package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"emby-go/internal/config"
)

func logoutRequest(a *App, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func TestLogoutRevokesOnlyCurrentSessionOnAllAliases(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	const other = "fixture-other-session"
	if err := a.db.SaveAccessToken(other); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/Sessions/Logout", "/sessions/logout", "/emby/Sessions/Logout", "/emby/sessions/logout"} {
		t.Run(path, func(t *testing.T) {
			token := "fixture-session-" + path
			if err := a.db.SaveAccessToken(token); err != nil {
				t.Fatal(err)
			}
			w := logoutRequest(a, http.MethodPost, path, token)
			if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
				t.Fatalf("logout status=%d body=%s", w.Code, w.Body.String())
			}
			if valid, err := a.db.HasAccessToken(token); err != nil || valid {
				t.Fatalf("not revoked: valid=%v err=%v", valid, err)
			}
			if w := logoutRequest(a, http.MethodGet, "/Users/Me", token); w.Code != http.StatusUnauthorized {
				t.Fatalf("revoked credential status=%d", w.Code)
			}
			if w := logoutRequest(a, http.MethodGet, "/Users/Me", other); w.Code != http.StatusOK {
				t.Fatalf("other session status=%d", w.Code)
			}
			// No tombstones yet: repeat requests are unauthenticated, not a public
			// idempotency exception that could authorize other endpoints.
			if w := logoutRequest(a, http.MethodPost, path, token); w.Code != http.StatusUnauthorized {
				t.Fatalf("replayed logout status=%d", w.Code)
			}
		})
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestLogoutPreservesAPIKey(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	key, err := a.db.CreateAPIKey("fixture key")
	if err != nil {
		t.Fatal(err)
	}
	if w := logoutRequest(a, http.MethodPost, "/Sessions/Logout", key.Key); w.Code != http.StatusNoContent {
		t.Fatalf("key logout status=%d", w.Code)
	}
	if valid, err := a.db.HasAPIKey(key.Key); err != nil || !valid {
		t.Fatalf("key changed: valid=%v err=%v", valid, err)
	}
	if w := logoutRequest(a, http.MethodGet, "/Users/Me", key.Key); w.Code != http.StatusOK {
		t.Fatalf("key no longer authorized: %d", w.Code)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestLogoutWriteFailureDoesNotReportSuccess(t *testing.T) {
	a, legacy, path := authorityApp(t)
	const token = "fixture-session-secret"
	if err := a.db.SaveAccessToken(token); err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	_, err = fixture.Exec(`CREATE TRIGGER fixture_reject_logout BEFORE DELETE ON access_tokens BEGIN SELECT RAISE(ABORT, 'fixture raw SQL error secret'); END`)
	if err != nil {
		t.Fatal(err)
	}
	w := logoutRequest(a, http.MethodPost, "/Sessions/Logout", token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "AUTH_UNAVAILABLE" || body["retryable"] != true {
		t.Fatalf("body=%v", body)
	}
	for _, secret := range []string{token, path, "fixture raw SQL error secret", "SQLITE"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
	if valid, err := a.db.HasAccessToken(token); err != nil || !valid {
		t.Fatalf("failed delete changed session: valid=%v err=%v", valid, err)
	}
	if _, err := fixture.Exec("DROP TRIGGER fixture_reject_logout"); err != nil {
		t.Fatal(err)
	}
	if w := logoutRequest(a, http.MethodPost, "/Sessions/Logout", token); w.Code != http.StatusNoContent {
		t.Fatalf("retry status=%d", w.Code)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestLogoutPersistsAcrossReopen(t *testing.T) {
	a, legacy, path := authorityApp(t)
	const revoked, other = "fixture-revoked-session", "fixture-retained-session"
	for _, token := range []string{revoked, other} {
		if err := a.db.SaveAccessToken(token); err != nil {
			t.Fatal(err)
		}
	}
	if w := logoutRequest(a, http.MethodPost, "/Sessions/Logout", revoked); w.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d", w.Code)
	}
	a.Close()
	// Retain the cache that claims all old credentials exist across the reopen.
	reopened, err := newApp(config.Config{DBPath: path}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if w := logoutRequest(reopened, http.MethodGet, "/Users/Me", revoked); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session revived: %d", w.Code)
	}
	if w := logoutRequest(reopened, http.MethodGet, "/Users/Me", other); w.Code != http.StatusOK {
		t.Fatalf("other session status=%d", w.Code)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestLogoutRejectsMissingUnknownAndUnavailableCredentials(t *testing.T) {
	a, _, _ := authorityApp(t)
	for _, token := range []string{"", "fixture-unknown-session"} {
		if w := logoutRequest(a, http.MethodPost, "/Sessions/Logout", token); w.Code != http.StatusUnauthorized {
			t.Fatalf("unknown credential status=%d", w.Code)
		}
	}
	a.Close()
	if w := logoutRequest(a, http.MethodPost, "/Sessions/Logout", "fixture-unknown-session"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable authority status=%d", w.Code)
	}
}
