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

func setupCodeApp(t *testing.T, code string, proxies []string) (*App, error) {
	t.Helper()
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "setup.db"), SetupCode: code, TrustedProxies: proxies}, cache.NewMemory(16))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { a.Close() })
	return a, nil
}

func initializeRequest(a *App, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/initialize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func TestInitializeRequiresSetupCode(t *testing.T) {
	a, err := setupCodeApp(t, "setup-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	var status struct {
		Initialized       bool `json:"initialized"`
		SetupCodeRequired bool `json:"setup_code_required"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !status.SetupCodeRequired || status.Initialized {
		t.Fatalf("status=%s err=%v", w.Body.String(), err)
	}

	if w := initializeRequest(a, `{"Username":"admin","Pw":"password-1234"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "SETUP_CODE_INVALID") {
		t.Fatalf("missing code status=%d body=%s", w.Code, w.Body.String())
	}
	if w := initializeRequest(a, `{"Username":"admin","Pw":"password-1234","Code":"wrong"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong code status=%d body=%s", w.Code, w.Body.String())
	}
	if initialized, err := a.db.HasAdministrator(); err != nil || initialized {
		t.Fatalf("administrator created before code accepted: initialized=%v err=%v", initialized, err)
	}
	if w := initializeRequest(a, `{"Username":"admin","Pw":"password-1234","Code":"setup-code"}`); w.Code != http.StatusNoContent {
		t.Fatalf("valid code status=%d body=%s", w.Code, w.Body.String())
	}
	if initialized, err := a.db.HasAdministrator(); err != nil || !initialized {
		t.Fatalf("administrator missing after initialize: initialized=%v err=%v", initialized, err)
	}
}

func TestTrustedProxiesValidatedAtStartup(t *testing.T) {
	if _, err := setupCodeApp(t, "", []string{"not-a-cidr"}); err == nil || !strings.Contains(err.Error(), "trusted_proxies") {
		t.Fatalf("invalid proxy config accepted: err=%v", err)
	}
	a, err := setupCodeApp(t, "", []string{"127.0.0.1/32", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("valid proxy config rejected: %v", err)
	}
	// 受信代理场景下 X-Forwarded-For 参与 ClientIP 计算，限流按真实客户端 IP 分桶。
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("trusted proxy request status=%d", w.Code)
	}
}

func TestInitializeSourceAllowlist(t *testing.T) {
	open := func(allow []string) (*App, error) {
		return newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "init.db"), InitializeAllowFrom: allow}, cache.NewMemory(16))
	}
	if _, err := open([]string{"bad-cidr"}); err == nil || !strings.Contains(err.Error(), "initialize_allow_from") {
		t.Fatalf("invalid allowlist accepted: err=%v", err)
	}
	a, err := open([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	body := `{"Username":"admin","Pw":"password-1234"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/initialize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.1:1111"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "INIT_SOURCE_DENIED") {
		t.Fatalf("remote initialize status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/initialize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:2222"
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("loopback initialize status=%d body=%s", w.Code, w.Body.String())
	}
}
