package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// All App mutations go through real TCP HTTP and the public middleware. Only
// the translation provider is a controlled loopback service; no production I/O.
func TestStabilityLiveHTTPWorkflow(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`<movie><num>ABC-123</num><title>Original title</title><originaltitle>Original title</originaltitle><plot>Unchanged plot</plot></movie>`)
	nfoPath := filepath.Join(media, "ABC-123.nfo")
	if err := os.WriteFile(nfoPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "ABC-123.strm"), []byte("https://media.invalid/stability.mp4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := newApp(config.Config{DBPath: filepath.Join(root, "app.db")}, cache.NewMemory(128))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		app.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.WaitBackground(ctx); err != nil {
			t.Error(err)
		}
		app.Close()
	}()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, route, token string, body any, want int, headers ...map[string]string) map[string]any {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, srv.URL+route, input)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		for _, set := range headers {
			for k, v := range set {
				req.Header.Set(k, v)
			}
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, route, response.StatusCode, want, raw)
		}
		if want == 302 {
			if response.Header.Get("Location") != "https://media.invalid/stability.mp4" {
				t.Fatal("unexpected redirect")
			}
			return nil
		}
		var out map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("%s: %v: %s", route, err, raw)
			}
		}
		return out
	}
	login := func(name, password string) string {
		t.Helper()
		data := request("POST", "/Users/AuthenticateByName", "", map[string]any{"Username": name, "Pw": password}, 200)
		token, ok := data["AccessToken"].(string)
		if !ok || token == "" {
			t.Fatal("missing login token")
		}
		return token
	}
	password := "stability-password-123"
	request("POST", "/api/auth/initialize", "", map[string]any{"Username": "stable-admin", "Pw": password}, http.StatusNoContent)
	admin := login("stable-admin", password)
	request("POST", "/api/admin/libraries", admin, map[string]any{"name": "Live workflow", "path": media}, 200)
	libs := request("GET", "/api/admin/libraries", admin, nil, 200)["items"].([]any)
	libraryID := libs[0].(map[string]any)["Id"].(float64)
	scan := func() map[string]any {
		t.Helper()
		accepted := request("POST", fmt.Sprintf("/api/admin/tasks/scan?mode=incremental&library_id=%.0f", libraryID), admin, nil, 202)
		route := fmt.Sprintf("/api/admin/tasks/%.0f", accepted["task_id"].(float64))
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			state := request("GET", route, admin, nil, 200)
			switch state["status"] {
			case "succeeded":
				return state["result"].(map[string]any)
			case "failed", "skipped":
				t.Fatalf("scan failed: %+v", state)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("scan deadline")
		return nil
	}
	first := scan()
	if first["added"] != float64(1) {
		t.Fatalf("initial scan: %+v", first)
	}
	userResponse := request("POST", "/api/admin/users", admin, map[string]any{"name": "stable-viewer", "password": password}, 201)
	userID := userResponse["user"].(map[string]any)["id"].(float64)
	viewer := login("stable-viewer", password)
	items := func(token string) []any {
		t.Helper()
		return request("GET", "/Items?Limit=20", token, nil, 200)["Items"].([]any)
	}
	if got := len(items(viewer)); got != 0 {
		t.Fatalf("zero-grant viewer saw %d items", got)
	}
	grants := fmt.Sprintf("/api/admin/users/%.0f/libraries", userID)
	request("PUT", grants, admin, map[string]any{"library_ids": []float64{libraryID}}, 200)
	visible := items(viewer)
	if len(visible) != 1 {
		t.Fatalf("granted items=%v", visible)
	}
	movieID := fmt.Sprint(visible[0].(map[string]any)["Id"])
	detail := fmt.Sprintf("/Users/%.0f/Items/%s", userID, movieID)
	request("GET", detail, viewer, nil, 200)
	request("GET", "/Videos/"+movieID+"/stream", viewer, nil, 302)
	playing := map[string]any{"ItemId": movieID, "PlaySessionId": "live-stability-session", "PositionTicks": int64(1234567)}
	request("POST", "/Sessions/Playing", viewer, playing, 204)
	request("POST", "/Sessions/Playing/Stopped", viewer, playing, 204)
	request("POST", "/Sessions/Playing/Stopped", viewer, playing, 204)
	data := request("GET", detail, viewer, nil, 200)["UserData"].(map[string]any)
	if data["PlayCount"] != float64(1) {
		t.Fatalf("stop replay changed play count: %+v", data)
	}
	// Revoke after a warm list/detail cache. The same session must immediately
	// lose the object without losing its private watch state when re-granted.
	request("PUT", grants, admin, map[string]any{"library_ids": []int{}}, 200)
	if len(items(viewer)) != 0 {
		t.Fatal("warm list survived grant revocation")
	}
	request("GET", detail, viewer, nil, 404)
	request("PUT", grants, admin, map[string]any{"library_ids": []float64{libraryID}}, 200)
	if request("GET", detail, viewer, nil, 200)["UserData"].(map[string]any)["PlayCount"] != float64(1) {
		t.Fatal("watch state lost after grant change")
	}
	second := scan()
	if second["unchanged_groups"] != float64(1) || second["index_changed"] == true {
		t.Fatalf("second scan: %+v", second)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/translate" || r.Header.Get("Authorization") != "Bearer stability-key" {
			http.Error(w, "invalid translation request", 400)
			return
		}
		var input struct {
			Text []string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Text) != 1 || input.Text[0] != "Original title" {
			http.Error(w, "invalid source", 400)
			return
		}
		if calls.Add(1) == 1 {
			http.Error(w, "controlled provider failure", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"translations":[{"text":"独立译文","detected_source_language":"EN"}]}`)
	}))
	defer provider.Close()
	request("PUT", "/api/admin/scrape/settings", admin, map[string]any{"translate": map[string]any{"api_url": provider.URL, "api_key": "stability-key"}}, 200)
	translate := func() map[string]any {
		t.Helper()
		accepted := request("POST", "/api/admin/translate/run", admin, map[string]any{"library_id": libraryID, "fields": []string{"title"}}, 202)
		route := fmt.Sprintf("/api/admin/translate/status?task_id=%.0f", accepted["task_id"].(float64))
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			state := request("GET", route, admin, nil, 200)
			if state["running"] == false {
				return state
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("translation deadline")
		return nil
	}
	failed := translate()
	if failed["failed"] != float64(1) {
		t.Fatalf("provider failure not surfaced: %+v", failed)
	}
	raw, err := os.ReadFile(nfoPath)
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("failed translation modified NFO")
	}
	done := translate()
	if done["success"] != float64(1) || done["index_refresh_failed"] == true {
		t.Fatalf("translation retry: %+v", done)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	translated := items(viewer)
	if len(translated) != 1 || fmt.Sprint(translated[0].(map[string]any)["Id"]) != movieID || !strings.Contains(fmt.Sprint(translated[0]), "独立译文") {
		t.Fatalf("translated index/cache not refreshed: %+v", translated)
	}
	// A real interactive rotation exposes one new secret and invalidates the old
	// key, while ending the login session leaves that independent key alive.
	key := request("POST", "/api/admin/apikeys", admin, map[string]any{"name": "live-key"}, 200)
	keyID := key["id"].(string)
	oldKey := key["key"].(string)
	etag := fmt.Sprintf(`"ak-%s-1"`, strings.TrimPrefix(keyID, "ak_"))
	rotated := request("POST", "/api/admin/apikeys/"+keyID+"/rotate", admin, map[string]any{"current_password": password, "reason": "stability integration"}, 200, map[string]string{"If-Match": etag})
	request("GET", "/api/admin/libraries", oldKey, nil, 401)
	newKey := rotated["key"].(string)
	request("GET", "/api/admin/libraries", newKey, nil, 200)
	request("POST", "/api/auth/logout", admin, nil, http.StatusNoContent)
	request("GET", "/api/admin/libraries", admin, nil, 401)
	request("GET", "/api/admin/libraries", newKey, nil, 200)
	t.Log("real HTTP: initialize/login → scan → grant/revoke/cache → playback/stop idempotency → incremental → failed/retried translation → key rotation/logout")
}
