package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// TestSecurityAuthLimitsAndUnlock 覆盖 R1 的管理面：查看当前限流状态、
// 组合阈值返回 429+Retry-After、管理员用交互会话和当前密码解锁指定账号。
func TestSecurityAuthLimitsAndUnlock(t *testing.T) {
	root := t.TempDir()
	a, err := newApp(config.Config{
		DBPath: filepath.Join(root, "test.db"),
		Security: config.SecurityConfig{Auth: config.AuthGuardSettings{
			IPRequestBurst: 200, GlobalRequestBurst: 400, BcryptConcurrency: 8,
		}},
	}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	do := func(method, path, body, token string) (int, http.Header, map[string]any) {
		req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, resp.Header, v
	}

	if code, _, _ := do("POST", "/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`, ""); code != http.StatusNoContent {
		t.Fatalf("initialize: %d", code)
	}
	if _, err := a.db.CreateUser("victim", "password-1234", false); err != nil {
		t.Fatal(err)
	}
	code, _, v := do("POST", "/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`, "")
	if code != http.StatusOK {
		t.Fatalf("admin login: %d %v", code, v)
	}
	token, _ := v["AccessToken"].(string)
	if token == "" {
		t.Fatal("missing access token")
	}

	// 5 次错误密码触发组合阈值，第 6 次必须在解析密码前被拒。
	for i := range 5 {
		if code, _, _ := do("POST", "/Users/AuthenticateByName", `{"Username":"victim","Pw":"wrong-password"}`, ""); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, code)
		}
	}
	code, headers, limited := do("POST", "/Users/AuthenticateByName", `{"Username":"victim","Pw":"wrong-password"}`, "")
	if code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d %v", code, limited)
	}
	if limited["code"] != "AUTH_RATE_LIMITED" {
		t.Fatalf("unexpected error code: %v", limited)
	}
	if retry, err := strconv.Atoi(headers.Get("Retry-After")); err != nil || retry <= 0 {
		t.Fatalf("Retry-After must be positive: %q", headers.Get("Retry-After"))
	}

	code, _, limits := do("GET", "/api/admin/security/auth-limits", "", token)
	if code != http.StatusOK {
		t.Fatalf("auth-limits: %d %v", code, limits)
	}
	if limits["backend"] != "memory" {
		t.Fatalf("unexpected backend: %v", limits["backend"])
	}
	counters, _ := limits["counters"].(map[string]any)
	if rejected, _ := counters["rejected_combo"].(float64); rejected < 1 {
		t.Fatalf("combo rejection not counted: %v", limits["counters"])
	}

	// 再认证失败不得解锁，且必须返回 401 而不是泄漏密码是否正确。
	unlockBody := `{"target_type":"account","value":"victim","reason":"误报恢复","current_password":"wrong-password"}`
	code, _, failed := do("POST", "/api/admin/security/unlock", unlockBody, token)
	if code != http.StatusUnauthorized || failed["code"] != "AUTH_REVERIFY_FAILED" {
		t.Fatalf("unlock with wrong password: %d %v", code, failed)
	}
	unlockBody = `{"target_type":"account","value":"victim","reason":"误报恢复","current_password":"password-1234"}`
	if code, _, unlocked := do("POST", "/api/admin/security/unlock", unlockBody, token); code != http.StatusOK || unlocked["unlocked"] != true {
		t.Fatalf("unlock: %d %v", code, unlocked)
	}
	if code, _, _ := do("POST", "/Users/AuthenticateByName", `{"Username":"victim","Pw":"password-1234"}`, ""); code != http.StatusOK {
		t.Fatalf("victim still locked after unlock: %d", code)
	}
}
