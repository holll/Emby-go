package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthIdentityAuditsOnlyCommittedTokenIssuance(t *testing.T) {
	for _, mode := range []struct {
		name, statement string
		status          int
	}{
		{"authentication-changed", "SELECT RAISE(IGNORE)", http.StatusUnauthorized},
		{"storage-failure", "SELECT RAISE(ABORT,'injected token write failure')", http.StatusInternalServerError},
	} {
		t.Run(mode.name, func(t *testing.T) {
			a, _, path := authorityApp(t)
			seedOwner(t, a)
			fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()
			if _, err := fixture.Exec("CREATE TRIGGER reject_auth_identity_mint BEFORE INSERT ON access_tokens BEGIN " + mode.statement + "; END"); err != nil {
				t.Fatal(err)
			}
			const ip = "198.51.100.9"
			seedTicket, _ := a.authGuard.AdmitRequest(ip)
			if seedTicket == nil {
				t.Fatal("guard rejected seed admission")
			}
			if retry := a.authGuard.AttachAccount(seedTicket, "admin"); retry > 0 {
				t.Fatalf("guard rejected seed attach: %v", retry)
			}
			a.authGuard.Finish(seedTicket, authOutcomeInvalidPassword)
			login := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"admin","Pw":"fixture-pass-123"}`))
				req.RemoteAddr = ip + ":1234"
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, req)
				return w
			}
			if w := login(); w.Code != mode.status {
				t.Fatalf("failed mint status=%d body=%s", w.Code, w.Body.String())
			}
			last, err := a.db.UserLastLogin()
			if err != nil || last[1] != "" {
				t.Fatalf("failed mint advanced last login: %v %v", last, err)
			}
			events, err := a.db.LoginEvents(1, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("failed mint recorded success: %v %v", events, err)
			}
			a.authGuard.mu.Lock()
			ipFailures := a.authGuard.failures[authIPKey(ip)].failures
			accountFailures := a.authGuard.failures[authAccountDigest("admin")].failures
			a.authGuard.mu.Unlock()
			if ipFailures != 1 || accountFailures != 1 {
				t.Fatalf("failed mint reset limiter: ip=%d account=%d", ipFailures, accountFailures)
			}
			if _, err := fixture.Exec("DROP TRIGGER reject_auth_identity_mint"); err != nil {
				t.Fatal(err)
			}
			if w := login(); w.Code != http.StatusOK {
				t.Fatalf("successful mint status=%d body=%s", w.Code, w.Body.String())
			}
			last, err = a.db.UserLastLogin()
			if err != nil || last[1] == "" {
				t.Fatalf("successful mint missing last login: %v %v", last, err)
			}
			a.authGuard.mu.Lock()
			remainingIP := a.authGuard.failures[authIPKey(ip)].failures
			_, accountLeft := a.authGuard.failures[authAccountDigest("admin")]
			a.authGuard.mu.Unlock()
			if remainingIP != 1 {
				t.Fatalf("successful mint cleared IP spray history: ip=%d", remainingIP)
			}
			if accountLeft {
				t.Fatal("successful mint did not clear account failures")
			}
		})
	}
}

func TestAuthIdentityPasswordChangeAndWebLogout(t *testing.T) {
	a, _, _ := authorityApp(t)
	seedOwner(t, a)
	login := func(password string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"admin","Pw":"`+password+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct{ AccessToken string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.AccessToken == "" {
			t.Fatal("missing token")
		}
		return body.AccessToken
	}
	old := login("fixture-pass-123")
	other := login("fixture-pass-123")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(`{"current_password":"fixture-pass-123","new_password":"changed-password-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", old)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("password status=%d body=%s", w.Code, w.Body.String())
	}
	for _, token := range []string{old, other} {
		if w := logoutRequest(a, http.MethodGet, "/Users/Me", token); w.Code != http.StatusUnauthorized {
			t.Fatalf("old session remains: %d", w.Code)
		}
	}
	current := login("changed-password-1")
	retained := login("changed-password-1")
	key, err := a.db.CreateAPIKey("logout-retained")
	if err != nil {
		t.Fatal(err)
	}
	if w := logoutRequest(a, http.MethodPost, "/api/auth/logout", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("public logout status=%d", w.Code)
	}
	w = logoutRequest(a, http.MethodPost, "/api/auth/logout", current)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("web logout status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("logout response cacheable")
	}
	if w := logoutRequest(a, http.MethodGet, "/Users/Me", current); w.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out token valid: %d", w.Code)
	}
	for _, token := range []string{retained, key.Key} {
		if w := logoutRequest(a, http.MethodGet, "/Users/Me", token); w.Code != http.StatusOK {
			t.Fatalf("other credential changed: %d", w.Code)
		}
	}
	if w := logoutRequest(a, http.MethodPost, "/api/auth/logout", current); w.Code != http.StatusUnauthorized {
		t.Fatalf("replay logout status=%d", w.Code)
	}
}
