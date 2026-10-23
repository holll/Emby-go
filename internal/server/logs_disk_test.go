package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"emby-go/internal/logging"
)

func TestAdminDiskLogsContractAndValidation(t *testing.T) {
	dir := t.TempDir()
	logging.SetAppLogDir(dir)
	t.Cleanup(func() { logging.SetAppLogDir("") })
	line := `time=2026-10-23T08:00:00+08:00 level=INFO msg="探测进度 token=CANARY_TOKEN"` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "app-2026-10-23.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	a, token := logsTestApp(t)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Emby-Token", token)
		w := httptest.NewRecorder()
		a.router.ServeHTTP(w, req)
		return w
	}

	w := get("/api/admin/logs/disk?kind=app&limit=10")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Source string `json:"source"`
		Kind   string `json:"kind"`
		Items  []struct {
			ID      string `json:"id"`
			Time    string `json:"time"`
			Level   string `json:"level"`
			Message string `json:"message"`
		} `json:"items"`
		NextAfter  string `json:"next_after"`
		NextBefore string `json:"next_before"`
		HasMore    bool   `json:"has_more"`
		Reset      bool   `json:"reset"`
		Gap        bool   `json:"gap"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "disk" || body.Kind != "app" || len(body.Items) != 1 {
		t.Fatalf("bad response: %s", w.Body.String())
	}
	if body.Items[0].ID == "" || body.Items[0].Level != "INFO" || body.NextAfter == "" || body.NextBefore == "" {
		t.Fatalf("bad item/cursors: %s", w.Body.String())
	}
	if body.Items[0].Message == "探测进度 token=CANARY_TOKEN" || body.Items[0].Message == "" {
		t.Fatalf("message not sanitized: %q", body.Items[0].Message)
	}

	for _, path := range []string{
		"/api/admin/logs/disk?kind=request",
		"/api/admin/logs/disk?kind=app&limit=0",
		"/api/admin/logs/disk?kind=app&limit=501",
		"/api/admin/logs/disk?after=x&before=y",
		"/api/admin/logs/disk?after=forged",
	} {
		if got := get(path); got.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, got.Code, got.Body.String())
		}
	}

	anon := httptest.NewRecorder()
	a.router.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/api/admin/logs/disk", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("未认证 status=%d", anon.Code)
	}
}
