package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"

	"github.com/gin-gonic/gin"
)

// Pretend legacy Redis contains every credential, even after DB revocation.
// Any credential cache access is itself a regression, regardless of HTTP status.
type legacyCredentialCache struct {
	cache.Cache
	calls atomic.Int64
}

func (c *legacyCredentialCache) Get(key string) ([]byte, bool) {
	if strings.HasPrefix(key, "token:") || strings.HasPrefix(key, "apikey:") {
		c.calls.Add(1)
		return []byte("1"), true
	}
	return c.Cache.Get(key)
}

func (c *legacyCredentialCache) Set(key string, value []byte, ttl time.Duration) {
	if strings.HasPrefix(key, "token:") || strings.HasPrefix(key, "apikey:") {
		c.calls.Add(1)
	}
	c.Cache.Set(key, value, ttl)
}

// sessionDigest 复算登录会话落库摘要（服务端不再存明文）。
func sessionDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func authorityApp(t *testing.T) (*App, *legacyCredentialCache, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authority.db")
	legacy := &legacyCredentialCache{Cache: cache.NewMemory(16)}
	a, err := newApp(config.Config{DBPath: path}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, legacy, path
}

func authorityRouter(a *App) *gin.Engine {
	r := gin.New()
	r.GET("/private", a.requireAuth, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return r
}

func authorityRequest(r http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAuthIgnoresLegacyPositiveCaches(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	r := authorityRouter(a)
	for _, token := range []string{"", "fixture-unknown-session", "fixture-deleted-key"} {
		w := authorityRequest(r, token)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != "unauthorized" || body["code"] != "AUTH_INVALID" {
			t.Fatalf("body=%v", body)
		}
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestAuthConsultsStoreAfterCredentialDeletion(t *testing.T) {
	for _, kind := range []string{"session", "key"} {
		t.Run(kind, func(t *testing.T) {
			a, legacy, path := authorityApp(t)
			seedOwner(t, a)
			token := "fixture-session"
			if kind == "session" {
				if err := a.db.SaveAccessToken(token); err != nil {
					t.Fatal(err)
				}
			} else {
				key, err := a.db.CreateAPIKey("fixture")
				if err != nil {
					t.Fatal(err)
				}
				token = key.Key
			}
			r := authorityRouter(a)
			if w := authorityRequest(r, token); w.Code != http.StatusNoContent {
				t.Fatalf("valid status=%d", w.Code)
			}
			if kind == "session" {
				fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
				if err != nil {
					t.Fatal(err)
				}
				_, err = fixture.Exec("DELETE FROM access_tokens WHERE token=?", sessionDigest(token))
				fixture.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else if err := a.db.DeleteAPIKey(token); err != nil {
				t.Fatal(err)
			}
			// New requests after the committed delete must never use stale positives.
			for i := 0; i < 3; i++ {
				if w := authorityRequest(r, token); w.Code != http.StatusUnauthorized {
					t.Fatalf("deleted credential status=%d", w.Code)
				}
			}
			if calls := legacy.calls.Load(); calls != 0 {
				t.Fatalf("credential cache used %d times", calls)
			}
		})
	}
}

func TestAuthDatabaseFailureIsUnavailable(t *testing.T) {
	for _, mode := range []string{"closed store", "key lookup failure"} {
		t.Run(mode, func(t *testing.T) {
			a, legacy, path := authorityApp(t)
			if mode == "closed store" {
				a.Close()
			} else {
				fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
				if err != nil {
					t.Fatal(err)
				}
				_, err = fixture.Exec("DROP TABLE api_keys")
				fixture.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			w := authorityRequest(authorityRouter(a), "fixture-secret-do-not-echo")
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
			for _, sensitive := range []string{"fixture-secret-do-not-echo", path, "sql:", "no such table"} {
				if strings.Contains(w.Body.String(), sensitive) {
					t.Fatalf("error leaked %q", sensitive)
				}
			}
			if calls := legacy.calls.Load(); calls != 0 {
				t.Fatalf("credential cache used %d times", calls)
			}
		})
	}
}

func TestAuthPreservesCredentialTransports(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	seedOwner(t, a)
	const token = "fixture-valid-session"
	if err := a.db.SaveAccessToken(token); err != nil {
		t.Fatal(err)
	}
	key, err := a.db.CreateAPIKey("transport fixture")
	if err != nil {
		t.Fatal(err)
	}
	r := authorityRouter(a)
	for _, token := range []string{token, key.Key} {
		for _, transport := range []struct{ header, value, query string }{
			{"X-Emby-Token", token, ""},
			{"X-MediaBrowser-Token", token, ""},
			{"", "", "api_key=" + token},
			{"", "", "ApiKey=" + token},
			{"", "", "X-Emby-Token=" + token},
			{"X-Emby-Authorization", `MediaBrowser Client="fixture", Token="` + token + `"`, ""},
			{"Authorization", `MediaBrowser Client="fixture", Token="` + token + `"`, ""},
		} {
			req := httptest.NewRequest(http.MethodGet, "/private?"+transport.query, nil)
			if transport.header != "" {
				req.Header.Set(transport.header, transport.value)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusNoContent {
				t.Fatalf("transport=%s/%s status=%d", transport.header, transport.query, w.Code)
			}
		}
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestAuthRevocationAllowsOnlyAlreadyAuthorizedRequest(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	seedOwner(t, a)
	key, err := a.db.CreateAPIKey("revocation fixture")
	if err != nil {
		t.Fatal(err)
	}
	authorized, release, done := make(chan struct{}), make(chan struct{}), make(chan int, 1)
	defer close(release)
	r := gin.New()
	r.GET("/private", a.requireAuth, func(c *gin.Context) {
		close(authorized)
		<-release
		c.Status(http.StatusNoContent)
	})
	go func() { done <- authorityRequest(r, key.Key).Code }()
	select {
	case <-authorized:
	case <-time.After(10 * time.Second):
		t.Fatal("request did not pass authentication")
	}
	if err := a.db.DeleteAPIKey(key.Key); err != nil {
		t.Fatal(err)
	}
	// The older authorized handler is deliberately held while a new query starts.
	if w := authorityRequest(authorityRouter(a), key.Key); w.Code != http.StatusUnauthorized {
		t.Fatalf("post-revocation status=%d", w.Code)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
	// Check completion after this test's deferred release, without asserting that
	// revocation cancels an already-authorized handler or an established stream.
	t.Cleanup(func() {
		select {
		case status := <-done:
			if status != http.StatusNoContent {
				t.Errorf("in-flight status=%d", status)
			}
		case <-time.After(10 * time.Second):
			t.Error("authorized request failed to finish")
		}
	})
}

func TestAuthPreservesPriorityAndHistoricalSessions(t *testing.T) {
	a, legacy, path := authorityApp(t)
	seedOwner(t, a)
	const token = "fixture-historical-session"
	if err := a.db.SaveAccessToken(token); err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Exec("UPDATE access_tokens SET created_at=datetime('now','-30 days') WHERE token=?", sessionDigest(token))
	fixture.Close()
	if err != nil {
		t.Fatal(err)
	}
	r := authorityRouter(a)
	if w := authorityRequest(r, token); w.Code != http.StatusNoContent {
		t.Fatalf("historical session status=%d", w.Code)
	}
	for _, query := range []string{"api_key=" + token, "ApiKey=" + token, "X-Emby-Token=" + token} {
		req := httptest.NewRequest(http.MethodGet, "/private?"+query, nil)
		req.Header.Set("X-Emby-Token", "fixture-invalid-higher-priority")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("conflicting transport status=%d", w.Code)
		}
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}

func TestAuthenticateDoesNotPopulateCredentialCache(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	if err := a.db.InitializeAdministrator("fixture-admin", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/login", a.authenticate)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"Username":"fixture-admin","Pw":"fixture-password"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AccessToken == "" {
		t.Fatal("missing access token")
	}
	if valid, err := a.db.HasAccessToken(body.AccessToken); err != nil || !valid {
		t.Fatalf("token not persisted: valid=%v err=%v", valid, err)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential cache used %d times", calls)
	}
}
