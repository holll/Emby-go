package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func apiKeyRotationRequest(a *App, method, path, token, ifMatch, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.router.ServeHTTP(w, req)
	return w
}

func TestAdminRotateAPIKeyRequiresReauthAndVersion(t *testing.T) {
	a, token := logsTestApp(t)
	create := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys", token, "", `{"name":"script"}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	stable := strings.TrimPrefix(created.ID, "ak_")
	if created.Version != 1 || len(stable) != 32 {
		t.Fatalf("created=%+v", created)
	}

	if w := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys/"+created.ID+"/rotate", token, "", `{"current_password":"fixture-pass-123","reason":"forgotten"}`); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d body=%s", w.Code, w.Body.String())
	}
	if w := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys/"+created.ID+"/rotate", token, `"bogus"`, `{"current_password":"fixture-pass-123","reason":"forgotten"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match status=%d body=%s", w.Code, w.Body.String())
	}
	if w := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys/"+created.ID+"/rotate", token, fmt.Sprintf(`"ak-%s-1"`, stable), `{"current_password":"wrong-password","reason":"forgotten"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d body=%s", w.Code, w.Body.String())
	}
	// 长期 API key 不能代替交互会话做轮转。
	if w := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys/"+created.ID+"/rotate", created.Key, fmt.Sprintf(`"ak-%s-1"`, stable), `{"current_password":"fixture-pass-123","reason":"forgotten"}`); w.Code != http.StatusForbidden {
		t.Fatalf("api-key session status=%d body=%s", w.Code, w.Body.String())
	}

	rotate := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys/"+created.ID+"/rotate", token, fmt.Sprintf(`"ak-%s-1"`, stable), `{"current_password":"fixture-pass-123","reason":"forgotten"}`)
	if rotate.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", rotate.Code, rotate.Body.String())
	}
	var rotated struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		KeyPrefix string `json:"key_prefix"`
		Version   int64  `json:"version"`
		RotatedAt string `json:"rotated_at"`
	}
	if err := json.Unmarshal(rotate.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.ID != created.ID || rotated.Version != 2 || rotated.RotatedAt == "" || rotated.Key == created.Key || len(rotated.Key) != 48 {
		t.Fatalf("rotated=%+v", rotated)
	}
	if got := rotate.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control=%q", got)
	}
	// 旧 key 立即失效，新 key 可用。
	if w := apiKeyRotationRequest(a, http.MethodGet, "/Users/1/Views", created.Key, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("old key still valid status=%d", w.Code)
	}
	if w := apiKeyRotationRequest(a, http.MethodGet, "/Users/1/Views", rotated.Key, "", ""); w.Code != http.StatusOK {
		t.Fatalf("new key invalid status=%d body=%s", w.Code, w.Body.String())
	}
	// 再次用旧版本轮转必须 412，且已提交的新 key 保持有效。
	if w := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys/"+created.ID+"/rotate", token, fmt.Sprintf(`"ak-%s-1"`, stable), `{"current_password":"fixture-pass-123","reason":"retry"}`); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale rotate status=%d body=%s", w.Code, w.Body.String())
	}
	if w := apiKeyRotationRequest(a, http.MethodGet, "/Users/1/Views", rotated.Key, "", ""); w.Code != http.StatusOK {
		t.Fatalf("committed key invalidated by failed rotate status=%d", w.Code)
	}
	// 列表保留同一记录身份并带版本/时间，不再回显摘要。
	list := apiKeyRotationRequest(a, http.MethodGet, "/api/admin/apikeys", token, "", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), rotated.Key) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
}
