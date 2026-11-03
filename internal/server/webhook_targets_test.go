package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func webhookTargetsRequest(a *App, method, token, ifMatch, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/admin/webhook/targets", strings.NewReader(body))
	req.Header.Set("X-Emby-Token", token)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.router.ServeHTTP(w, req)
	return w
}

type webhookTargetsResponse struct {
	Version int    `json:"version"`
	ETag    string `json:"config_etag"`
	Multi   bool   `json:"multi_target"`
	Targets []struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		Enabled        bool     `json:"enabled"`
		URL            string   `json:"url"`
		URLConfigured  bool     `json:"url_configured"`
		Events         []string `json:"events"`
		Format         string   `json:"format"`
		TimeoutSeconds int      `json:"timeout_seconds"`
		AllowPrivate   bool     `json:"allow_private"`
	} `json:"targets"`
}

func decodeWebhookTargets(t *testing.T, w *httptest.ResponseRecorder) webhookTargetsResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("administrator webhook response must not be cached")
	}
	var body webhookTargetsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Targets []map[string]json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, target := range raw.Targets {
		var url string
		value, ok := target["url"]
		if !ok || string(value) == "null" || json.Unmarshal(value, &url) != nil {
			t.Fatal("every administrator target must include a string URL, even when empty")
		}
	}
	return body
}

func TestWebhookTargetsPreconditionsMergeAndClear(t *testing.T) {
	a, token := logsTestApp(t)
	initial := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodGet, token, "", ""))
	if initial.Version != 2 || initial.Multi || len(initial.Targets) != 1 || initial.Targets[0].ID != legacyWebhookTargetID {
		t.Fatalf("initial=%+v", initial)
	}
	initialTag := webhookTargetsRequest(a, http.MethodGet, token, "", "").Header().Get("ETag")
	if initialTag == "" || !strings.HasPrefix(initialTag, `"wh2-`) {
		t.Fatalf("etag=%q", initialTag)
	}
	if w := webhookTargetsRequest(a, http.MethodPut, token, "", `{"targets":[]}`); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing if-match status=%d", w.Code)
	}
	// 首次保存把 legacy 目标升级为 v2；URL 可以随首次保存一起写入。
	body := fmt.Sprintf(`{"targets":[{"id":%q,"name":"旧目标","enabled":false,"url":"https://example.com/hook","events":["item.updated"],"format":"json","timeout_seconds":7,"allow_private":false}]}`, legacyWebhookTargetID)
	saved := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodPut, token, initialTag, body))
	if !saved.Multi || len(saved.Targets) != 1 || !saved.Targets[0].URLConfigured || saved.Targets[0].TimeoutSeconds != 7 {
		t.Fatalf("saved=%+v", saved)
	}
	loaded := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodGet, token, "", ""))
	if saved.Targets[0].URL != "https://example.com/hook" || loaded.Targets[0].URL != saved.Targets[0].URL {
		t.Fatal("administrator PUT and GET must return the saved URL")
	}
	// 旧单目标接口：GET 标注 multi_target，PUT 拒绝覆盖整个集合。
	legacyReq := httptest.NewRequest(http.MethodGet, "/api/admin/webhook/settings", nil)
	legacyReq.Header.Set("X-Emby-Token", token)
	legacyW := httptest.NewRecorder()
	a.router.ServeHTTP(legacyW, legacyReq)
	var legacyView map[string]any
	if legacyW.Code != http.StatusOK || json.Unmarshal(legacyW.Body.Bytes(), &legacyView) != nil || legacyView["multi_target"] != true {
		t.Fatalf("legacy view status=%d body=%s", legacyW.Code, legacyW.Body.String())
	}
	oldTag := legacyW.Header().Get("ETag")
	oldBody := `{"enabled":false,"events":["item.updated"],"format":"json","timeout_seconds":5,"allow_private":false}`
	oldSave := httptest.NewRequest(http.MethodPut, "/api/admin/webhook/settings", strings.NewReader(oldBody))
	oldSave.Header.Set("X-Emby-Token", token)
	oldSave.Header.Set("If-Match", oldTag)
	oldSave.Header.Set("Content-Type", "application/json")
	oldSaveW := httptest.NewRecorder()
	a.router.ServeHTTP(oldSaveW, oldSave)
	if oldSaveW.Code != http.StatusConflict {
		t.Fatalf("legacy put status=%d body=%s", oldSaveW.Code, oldSaveW.Body.String())
	}

	// 未提交 URL 表示保留；clear_url 才清除。按 ID 合并，不按数组位置。
	nextTag := webhookTargetsRequest(a, http.MethodGet, token, "", "").Header().Get("ETag")
	keep := fmt.Sprintf(`{"targets":[{"id":%q,"name":"保留目标","enabled":false,"events":["item.updated","item.rate"],"format":"json","timeout_seconds":5,"allow_private":false}]}`, legacyWebhookTargetID)
	if w := webhookTargetsRequest(a, http.MethodPut, token, nextTag, keep); w.Code != http.StatusOK {
		t.Fatalf("keep status=%d body=%s", w.Code, w.Body.String())
	}
	a.webhookMu.RLock()
	keptURL := a.webhookTargets[0].URL
	a.webhookMu.RUnlock()
	if keptURL != "https://example.com/hook" {
		t.Fatalf("url not retained: %q", keptURL)
	}
	// 过期 ETag 不允许覆盖。
	if w := webhookTargetsRequest(a, http.MethodPut, token, initialTag, keep); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale status=%d", w.Code)
	}
	// 未知/重复 ID 拒绝。
	unknown := `{"targets":[{"id":"wh_unknown","name":"x","enabled":false,"events":["item.updated"],"format":"json","timeout_seconds":5}]}`
	if w := webhookTargetsRequest(a, http.MethodPut, token, webhookTargetsRequest(a, http.MethodGet, token, "", "").Header().Get("ETag"), unknown); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", w.Code, w.Body.String())
	}
	// 清空集合必须显式确认。
	emptyTag := webhookTargetsRequest(a, http.MethodGet, token, "", "").Header().Get("ETag")
	if w := webhookTargetsRequest(a, http.MethodPut, token, emptyTag, `{"targets":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty status=%d", w.Code)
	}
	cleared := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodPut, token, emptyTag, `{"targets":[],"confirm_empty_targets":true}`))
	if len(cleared.Targets) != 0 || !cleared.Multi {
		t.Fatalf("cleared=%+v", cleared)
	}
	// 清空后 v2 仍权威：重启也不能复活 v1/YAML 地址。
	path := a.cfg.DBPath
	a.Close()
	reopened, err := newApp(config.Config{DBPath: path}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.webhookTargets) != 0 || !reopened.webhookMulti {
		t.Fatalf("reopened specs=%+v multi=%v", reopened.webhookTargets, reopened.webhookMulti)
	}
}

func TestWebhookTargetsAdminURLReadSaveReloadAndClear(t *testing.T) {
	a, token := logsTestApp(t)
	const legacyURL = "https://example.com/legacy-url-canary?key=legacy-query-canary"
	const savedURL = "https://example.com/saved-url-canary?key=saved-query-canary"

	// The multi-target view also exposes the URL adapted from legacy settings,
	// while the legacy settings API itself remains write-only.
	legacy := webhookSettingsCall(a, http.MethodGet, token, "", "")
	legacySave := webhookSettingsCall(a, http.MethodPut, token, legacy.Header().Get("ETag"), webhookSettingsBody(legacyURL, false, "item.updated"))
	if legacySave.Code != http.StatusOK || strings.Contains(legacySave.Body.String(), "legacy-url-canary") {
		t.Fatal("legacy settings must save successfully without exposing its URL")
	}
	initial := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodGet, token, "", ""))
	if initial.Multi || len(initial.Targets) != 1 || initial.Targets[0].URL != legacyURL || !initial.Targets[0].URLConfigured {
		t.Fatal("administrator must be able to read the adapted legacy URL")
	}
	body := fmt.Sprintf(`{"targets":[{"id":%q,"name":"saved","enabled":false,"url":%q,"events":["item.updated"],"format":"json","timeout_seconds":5}]}`, initial.Targets[0].ID, savedURL)
	saved := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodPut, token, initial.ETag, body))
	if !saved.Multi || saved.Targets[0].URL != savedURL || !saved.Targets[0].URLConfigured {
		t.Fatal("administrator PUT must return the saved URL")
	}

	cfg := a.cfg
	a.Close()
	reopened, err := newApp(cfg, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded := decodeWebhookTargets(t, webhookTargetsRequest(reopened, http.MethodGet, token, "", ""))
	if loaded.Targets[0].URL != savedURL || loaded.ETag != saved.ETag {
		t.Fatal("saved URL and ETag must survive restart")
	}
	// Both empty and whitespace-only URL values retain the saved URL.
	for _, blank := range []string{"", "  "} {
		body = fmt.Sprintf(`{"targets":[{"id":%q,"name":"saved","enabled":false,"url":%q,"events":["item.updated"],"format":"json","timeout_seconds":5}]}`, loaded.Targets[0].ID, blank)
		loaded = decodeWebhookTargets(t, webhookTargetsRequest(reopened, http.MethodPut, token, loaded.ETag, body))
		if loaded.Targets[0].URL != savedURL || !loaded.Targets[0].URLConfigured {
			t.Fatal("blank URL must retain the saved URL")
		}
	}
	body = fmt.Sprintf(`{"targets":[{"id":%q,"name":"saved","enabled":false,"clear_url":true,"events":["item.updated"],"format":"json","timeout_seconds":5}]}`, loaded.Targets[0].ID)
	cleared := decodeWebhookTargets(t, webhookTargetsRequest(reopened, http.MethodPut, token, loaded.ETag, body))
	if cleared.Targets[0].URL != "" || cleared.Targets[0].URLConfigured {
		t.Fatal("clear_url must return an empty URL and unset url_configured")
	}
	reopened.Close()
	again, err := newApp(cfg, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	loaded = decodeWebhookTargets(t, webhookTargetsRequest(again, http.MethodGet, token, "", ""))
	if loaded.Targets[0].URL != "" || loaded.Targets[0].URLConfigured {
		t.Fatal("cleared URL must remain empty after restart")
	}
}

func TestWebhookTargetsURLRequiresAdminAndNoStore(t *testing.T) {
	a, token := logsTestApp(t)
	initial := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodGet, token, "", ""))
	const url = "https://example.com/admin-url-canary?key=query-canary"
	body := fmt.Sprintf(`{"targets":[{"id":%q,"name":"admin","enabled":false,"url":%q,"events":["item.updated"],"format":"json","timeout_seconds":5}]}`, initial.Targets[0].ID, url)
	saved := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodPut, token, initial.ETag, body))
	user, err := a.db.CreateUser("ordinary-webhook", "test-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.SaveAccessTokenForUser("ordinary-webhook-token", user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for credential, want := range map[string]int{"": http.StatusUnauthorized, "invalid-token": http.StatusUnauthorized, "ordinary-webhook-token": http.StatusForbidden} {
			w := webhookTargetsRequest(a, method, credential, saved.ETag, body)
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s unauthorized status=%d cache=%q", method, w.Code, w.Header().Get("Cache-Control"))
			}
			for _, canary := range []string{url, "admin-url-canary", "query-canary"} {
				if strings.Contains(w.Body.String(), canary) {
					t.Fatal("unauthorized response exposed a webhook URL")
				}
			}
		}
	}
	loaded := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodGet, token, "", ""))
	if loaded.ETag != saved.ETag || loaded.Targets[0].URL != url {
		t.Fatal("unauthorized requests must not change saved configuration")
	}
}

func TestWebhookTargetsNewIDsAndDeleteConfirmation(t *testing.T) {
	a, token := logsTestApp(t)
	tag := webhookTargetsRequest(a, http.MethodGet, token, "", "").Header().Get("ETag")
	enabledDelete := `{"targets":[{"name":"新目标","enabled":true,"url":"https://example.com/new","events":["item.deleted"],"format":"json","timeout_seconds":5}]}`
	if w := webhookTargetsRequest(a, http.MethodPut, token, tag, enabledDelete); w.Code != http.StatusBadRequest {
		t.Fatalf("delete confirm status=%d body=%s", w.Code, w.Body.String())
	}
	withConfirm := `{"targets":[{"name":"新目标","enabled":true,"url":"https://example.com/new","events":["item.deleted"],"format":"json","timeout_seconds":5}],"confirm_delete":true}`
	body := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodPut, token, tag, withConfirm))
	if len(body.Targets) != 1 || !strings.HasPrefix(body.Targets[0].ID, "wh_") || len(body.Targets[0].ID) != 19 {
		t.Fatalf("targets=%+v", body.Targets)
	}
	// 第二次保存不提供 URL 时继续保留秘密。
	secondTag := webhookTargetsRequest(a, http.MethodGet, token, "", "").Header().Get("ETag")
	keepBody := fmt.Sprintf(`{"targets":[{"id":%q,"name":"改名","enabled":true,"events":["item.deleted"],"format":"json","timeout_seconds":6,"allow_private":false}],"confirm_delete":true}`, body.Targets[0].ID)
	kept := decodeWebhookTargets(t, webhookTargetsRequest(a, http.MethodPut, token, secondTag, keepBody))
	if !kept.Targets[0].URLConfigured || kept.Targets[0].Name != "改名" || kept.Targets[0].TimeoutSeconds != 6 {
		t.Fatalf("kept=%+v", kept.Targets)
	}
}
