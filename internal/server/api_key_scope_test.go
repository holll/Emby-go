package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAPIKeyScopePathBoundaries(t *testing.T) {
	for _, path := range []string{"/api/admin/users", "/emby/api/admin/users", "/healthz", "/admin", "/ItemsAdmin", "/emby/UsersAdmin", "/embyadmin/Items"} {
		if apiKeyScopeAllows(path) {
			t.Errorf("non-Emby path allowed: %s", path)
		}
	}
}

// API 密钥只用于 Emby 兼容接口：管理面与普通 Web API 一律 403 API_KEY_SCOPE，
// 交互会话不受影响。
func TestAPIKeyScopeLimitedToEmbyEndpoints(t *testing.T) {
	a, token := logsTestApp(t)
	create := apiKeyRotationRequest(a, http.MethodPost, "/api/admin/apikeys", token, "", `{"name":"script"}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Key == "" {
		t.Fatal("created key empty")
	}

	allowed := []string{"/Users/1/Views", "/Items?Limit=1", "/emby/System/Info/Public"}
	for _, prefix := range []string{"", "/emby"} {
		for _, path := range []string{"/Shows/NextUp", "/Episode/1/IntroSkipperSegments", "/MediaSegments/1", "/Items?Limit=1", "/Users/1/Views"} {
			allowed = append(allowed, prefix+path, prefix+strings.ToLower(path))
		}
	}
	for _, path := range allowed {
		if w := apiKeyRotationRequest(a, http.MethodGet, path, created.Key, "", ""); w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}

	denied := []string{"/api/admin/apikeys", "/api/admin/settings", "/api/admin/users", "/api/admin/scan/progress", "/api/auth/status", "/healthz", "/admin"}
	for _, path := range denied {
		w := apiKeyRotationRequest(a, http.MethodGet, path, created.Key, "", "")
		if w.Code != http.StatusForbidden || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"API_KEY_SCOPE"`) {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "API 密钥仅可用于 Emby 兼容接口") {
			t.Fatalf("%s missing error message: %s", path, w.Body.String())
		}
	}

	// Public handlers must retain their anonymous/session/invalid-token behavior.
	for _, path := range []string{"/api/auth/status", "/healthz", "/admin"} {
		anonymous := apiKeyRotationRequest(a, http.MethodGet, path, "", "", "")
		for _, credential := range []string{token, "invalid-token"} {
			w := apiKeyRotationRequest(a, http.MethodGet, path, credential, "", "")
			if w.Code != anonymous.Code {
				t.Fatalf("%s credential=%q status=%d, anonymous=%d", path, credential, w.Code, anonymous.Code)
			}
		}
	}
	for _, credential := range []string{"", token, created.Key, "invalid-token"} {
		for _, path := range []string{"/not-implemented", "/emby/api/admin/users"} {
			w := apiKeyRotationRequest(a, http.MethodGet, path, credential, "", "")
			if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("NoRoute %s credential=%q status=%d cache=%q", path, credential, w.Code, w.Header().Get("Cache-Control"))
			}
		}
	}

	// 同一交互会话仍可访问管理面。
	if w := apiKeyRotationRequest(a, http.MethodGet, "/api/admin/apikeys", token, "", ""); w.Code != http.StatusOK {
		t.Fatalf("session admin status=%d body=%s", w.Code, w.Body.String())
	}
}
