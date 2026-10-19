package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func TestSecurityHeadersOnPublicAndErrorResponses(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "headers.db")}, cache.NewMemory(8))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, path := range []string{"/api/health", "/definitely-missing"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" ||
			w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Permissions-Policy") == "" {
			t.Fatalf("%s 缺少安全头: %v", path, w.Header())
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy-Report-Only"), "default-src 'self'") {
			t.Fatalf("%s 缺少 CSP: %q", path, w.Header().Get("Content-Security-Policy-Report-Only"))
		}
	}
}

func TestAdminWriteActionsAreAudited(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "admin-audit.db")}, cache.NewMemory(8))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.db.InitializeAdministrator("admin", "password-1234"); err != nil {
		t.Fatal(err)
	}
	token := loginForToken(t, a)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Emby-Token", token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		return w
	}
	if w := do(http.MethodPost, "/api/admin/users", `{"name":"viewer","password":"viewer-pass-123"}`); w.Code != http.StatusCreated {
		t.Fatalf("create user=%d %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, "/api/admin/settings", ""); w.Code != http.StatusOK {
		t.Fatalf("settings=%d", w.Code)
	}
	w := do(http.MethodGet, "/api/admin/audit-events?limit=20", "")
	if w.Code != http.StatusOK {
		t.Fatalf("audit=%d %s", w.Code, w.Body.String())
	}
	var body struct {
		Events []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Status int    `json:"status"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Method != "POST" || body.Events[0].Path != "/api/admin/users" || body.Events[0].Status != http.StatusCreated {
		t.Fatalf("events=%+v", body.Events)
	}
}
