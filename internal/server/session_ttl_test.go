package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func ttlApp(t *testing.T, days int) *App {
	t.Helper()
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "ttl.db"), SessionTTLDays: days}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := a.db.InitializeAdministrator("admin", "password-1234"); err != nil {
		t.Fatal(err)
	}
	return a
}

func loginForToken(t *testing.T, a *App) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"admin","Pw":"password-1234"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.AccessToken == "" {
		t.Fatalf("login body=%s err=%v", w.Body.String(), err)
	}
	return body.AccessToken
}

func tokenRequest(a *App, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func TestLoginStoresSessionExpiry(t *testing.T) {
	a := ttlApp(t, 1)
	token := loginForToken(t, a)
	now := time.Now()
	if w := tokenRequest(a, http.MethodGet, "/api/admin/settings", token); w.Code != http.StatusOK {
		t.Fatalf("fresh token rejected status=%d", w.Code)
	}
	// ttl=0 只校验绝对过期：会话 23 小时后仍有效、25 小时后失效（校验会删除过期行）。
	if ok, err := a.db.ValidateAccessToken(token, now, 0); err != nil || !ok {
		t.Fatalf("fresh session rejected: ok=%v err=%v", ok, err)
	}
	if ok, err := a.db.ValidateAccessToken(token, now.Add(23*time.Hour), 0); err != nil || !ok {
		t.Fatalf("session expired too early: ok=%v err=%v", ok, err)
	}
	if ok, err := a.db.ValidateAccessToken(token, now.Add(25*time.Hour), 0); err != nil || ok {
		t.Fatalf("session outlived its TTL: ok=%v err=%v", ok, err)
	}

	// 过期会话：请求 401 且行被删除。
	if err := a.db.SaveAccessTokenExpiring("expired-session", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w := tokenRequest(a, http.MethodGet, "/api/admin/settings", "expired-session"); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status=%d", w.Code)
	}
	if ok, err := a.db.HasAccessToken("expired-session"); err != nil || ok {
		t.Fatalf("expired token not deleted: ok=%v err=%v", ok, err)
	}
}

func TestSessionTTLZeroKeepsToken(t *testing.T) {
	a := ttlApp(t, 0)
	token := loginForToken(t, a)
	if ok, err := a.db.ValidateAccessToken(token, time.Now().Add(10*365*24*time.Hour), 0); err != nil || !ok {
		t.Fatalf("ttl=0 session expired: ok=%v err=%v", ok, err)
	}
	if w := tokenRequest(a, http.MethodGet, "/api/admin/settings", token); w.Code != http.StatusOK {
		t.Fatalf("ttl=0 token rejected status=%d", w.Code)
	}
}

func TestRevokeAllSessionsKeepsAPIKeys(t *testing.T) {
	a := ttlApp(t, 30)
	first := loginForToken(t, a)
	second := loginForToken(t, a)
	keyReq := httptest.NewRequest(http.MethodPost, "/api/admin/apikeys", strings.NewReader(`{"name":"script"}`))
	keyReq.Header.Set("X-Emby-Token", first)
	keyReq.Header.Set("Content-Type", "application/json")
	keyW := httptest.NewRecorder()
	a.Handler().ServeHTTP(keyW, keyReq)
	if keyW.Code != http.StatusOK {
		t.Fatalf("create key status=%d body=%s", keyW.Code, keyW.Body.String())
	}
	var created struct{ Key string }
	if err := json.Unmarshal(keyW.Body.Bytes(), &created); err != nil || created.Key == "" {
		t.Fatalf("create key body=%s err=%v", keyW.Body.String(), err)
	}

	w := tokenRequest(a, http.MethodDelete, "/api/admin/access-tokens", first)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke all status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct{ Revoked int }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Revoked != 2 {
		t.Fatalf("revoke all body=%s err=%v", w.Body.String(), err)
	}
	for _, token := range []string{first, second} {
		if w := tokenRequest(a, http.MethodGet, "/api/admin/settings", token); w.Code != http.StatusUnauthorized {
			t.Fatalf("revoked token still valid status=%d", w.Code)
		}
	}
	if w := tokenRequest(a, http.MethodGet, "/api/admin/settings", created.Key); w.Code != http.StatusOK {
		t.Fatalf("api key revoked by session sweep status=%d", w.Code)
	}
}
