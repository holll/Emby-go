package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

const blocklistRoute = "/api/admin/security/ip-blocklist"

func blocklistCall(a *App, token, method, path, body, remote, forwarded string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Emby-Token", token)
	req.RemoteAddr = remote
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
		req.Header.Set("X-Real-IP", forwarded)
	}
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	return out
}
func blocklistFixture(t *testing.T) (*App, string) {
	t.Helper()
	a := contractTestApp(t)
	seedOwner(t, a)
	token := "ip-admin"
	if err := a.db.SaveAccessTokenForUser(token, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return a, token
}

func TestIPBlocklistAdminValidationAndRevocation(t *testing.T) {
	a, token := blocklistFixture(t)
	user := seedFixtureUser(t, a, "ordinary-ip", "password-123", false)
	if err := a.db.SaveAccessTokenForUser("ordinary-ip", user.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for cred, want := range map[string]int{"": 401, "ordinary-ip": 403} {
			if w := translationCall(a, cred, method, blocklistRoute, `{"network":"198.51.100.1"}`); w.Code != want {
				t.Fatalf("%s permission %d %s", method, w.Code, w.Body.String())
			}
		}
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"network":null}`, `{"network":"198.51.100.1","network":"198.51.100.2"}`, `{"Network":"198.51.100.1"}`, `{"network":"198.51.100.1","url":"x"}`, `{"network":"example.com"}`, `{"network":"198.51.100.1","note":"bad\nvalue"}`} {
		if w := translationCall(a, token, "POST", blocklistRoute, body); w.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// 回环与“全部地址”属于无法安全撤销的范围：明确 409，而不是含混的 400。
	for _, body := range []string{`{"network":"127.0.0.1"}`, `{"network":"127.0.0.0/8"}`, `{"network":"::/0"}`, `{"network":"0.0.0.0/0"}`, `{"network":"::1"}`} {
		if w := translationCall(a, token, "POST", blocklistRoute, body); w.Code != 409 {
			t.Fatalf("unsafe %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := translationCall(a, token, "POST", blocklistRoute, `{"network":"192.0.2.0/24"}`); w.Code != 409 {
		t.Fatalf("self lockout %d %s", w.Code, w.Body.String())
	}
	raw := `{"network":"198.51.100.1"}`
	body := &passwordIdentityReader{Reader: strings.NewReader(raw), beforeRead: func() {
		if err := a.db.DeleteAccessToken(token); err != nil {
			t.Fatal(err)
		}
	}}
	req := httptest.NewRequest("POST", blocklistRoute, body)
	req.ContentLength = int64(len(raw))
	req.Header.Set("X-Emby-Token", token)
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatalf("revoked admission %d %s", out.Code, out.Body.String())
	}
	rules, err := a.db.IPBlockRules(req.Context())
	if err != nil || len(rules) != 0 {
		t.Fatalf("revoked mutated %v %v", rules, err)
	}
}

func TestIPBlocklistMatchingCountsSpoofingAndPersistence(t *testing.T) {
	a, token := blocklistFixture(t)
	for _, network := range []string{"198.51.100.0/24", "2001:db8::/64"} {
		if w := translationCall(a, token, "POST", blocklistRoute, fmt.Sprintf(`{"network":%q,"note":"test"}`, network)); w.Code != 201 {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
	}
	if w := translationCall(a, token, "POST", blocklistRoute, `{"network":"198.51.100.2"}`); w.Code != 409 {
		t.Fatalf("overlap %d", w.Code)
	}
	// Untrusted headers cannot make a blocked peer appear innocent or frame a good peer.
	for _, remote := range []string{"198.51.100.8:2345", "[2001:db8::10]:2345", "[::ffff:198.51.100.9]:2345"} {
		for _, path := range []string{"/api/health", "/admin", "/Items/1/Images/Primary", "/web/app.js", blocklistRoute} {
			w := blocklistCall(a, token, "GET", path, "", remote, "203.0.113.1")
			if w.Code != 403 || !strings.Contains(w.Body.String(), "IP_BLOCKED") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("bypass %s %s: %d %s", remote, path, w.Code, w.Body.String())
			}
		}
	}
	if w := blocklistCall(a, "", "GET", "/api/health", "", "203.0.113.1:23", "198.51.100.8"); w.Code != 200 {
		t.Fatalf("spoof framed peer %d", w.Code)
	}
	var state struct {
		Items []struct {
			Network string `json:"network"`
			Blocked uint64 `json:"blocked"`
		} `json:"items"`
		Total uint64 `json:"total_blocked"`
	}
	out := translationCall(a, token, "GET", blocklistRoute, "")
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &state) != nil || state.Total != 15 || len(state.Items) != 2 || state.Items[0].Blocked != 10 || state.Items[1].Blocked != 5 {
		t.Fatalf("stats %d %s", out.Code, out.Body.String())
	}
	// Trusted proxy configuration uses Gin's validated forwarding chain.
	if err := a.router.SetTrustedProxies([]string{"203.0.113.10/32"}); err != nil {
		t.Fatal(err)
	}
	if w := blocklistCall(a, "", "GET", "/api/health", "", "203.0.113.10:23", "198.51.100.8"); w.Code != 403 {
		t.Fatalf("trusted forwarding %d", w.Code)
	}
	if w := blocklistCall(a, "", "GET", "/api/health", "", "203.0.113.11:23", "198.51.100.8"); w.Code != 200 {
		t.Fatalf("untrusted forwarding %d", w.Code)
	}
	// Reopening a real App must reload persisted policy but not process counters.
	again, err := newApp(a.cfg, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if w := blocklistCall(again, "", "GET", "/api/health", "", "198.51.100.8:23", ""); w.Code != 403 {
		t.Fatalf("restart lost rule %d", w.Code)
	}
	// Clearing a rule restores access immediately; other rule counts survive publication.
	if w := translationCall(a, token, "DELETE", blocklistRoute, `{"network":"198.51.100.0/24"}`); w.Code != 200 {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	if w := blocklistCall(a, "", "GET", "/api/health", "", "198.51.100.8:23", ""); w.Code != 200 {
		t.Fatalf("delete not effective %d", w.Code)
	}
	if w := translationCall(a, token, "DELETE", blocklistRoute, `{"network":"198.51.100.0/24"}`); w.Code != 404 {
		t.Fatalf("missing %d", w.Code)
	}
	out = translationCall(a, token, "GET", blocklistRoute, "")
	if json.Unmarshal(out.Body.Bytes(), &state) != nil || len(state.Items) != 1 || state.Items[0].Blocked != 5 || state.Total != 16 {
		t.Fatalf("preserved counts %s", out.Body.String())
	}
}

func TestIPBlocklistConcurrentCountersAndWriteFailure(t *testing.T) {
	a, token := blocklistFixture(t)
	if w := translationCall(a, token, "POST", blocklistRoute, `{"network":"198.51.100.0/24"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			w := blocklistCall(a, "", "GET", "/api/health", "", "198.51.100.7:88", "")
			if w.Code != 403 {
				t.Errorf("concurrent status %d", w.Code)
			}
		})
	}
	wg.Wait()
	if got := a.ipBlocks.total.Load(); got != 20 {
		t.Fatalf("lost counters %d", got)
	}
	// Failed persistence may never publish an in-memory-only deny rule.
	if err := a.db.SetKV("security:ip-blocklist:v1", "corrupt-policy"); err != nil {
		t.Fatal(err)
	}
	if w := translationCall(a, token, "POST", blocklistRoute, `{"network":"203.0.113.1"}`); w.Code != 500 || strings.Contains(w.Body.String(), "corrupt-policy") {
		t.Fatalf("failed persistence %d %s", w.Code, w.Body.String())
	}
	snap := a.ipBlocks.snapshot.Load()
	if len(snap.entries) != 1 || snap.entries[0].hits.Load() != 20 {
		t.Fatal("failed write changed snapshot")
	}
}

func TestIPBlocklistScopedPeerAndPreflightBoundaries(t *testing.T) {
	a, token := blocklistFixture(t)
	for _, network := range []string{"198.51.100.0/24", "fe80::/64"} {
		if w := translationCall(a, token, "POST", blocklistRoute, fmt.Sprintf(`{"network":%q}`, network)); w.Code != 201 {
			t.Fatalf("create %q %d %s", network, w.Code, w.Body.String())
		}
	}
	// Gin's net.ParseIP cannot represent a scoped IPv6 peer, so the policy must
	// fall back to the real TCP address instead of silently allowing it.
	if w := blocklistCall(a, "", "GET", "/api/health", "", "[fe80::123%eth0]:3456", ""); w.Code != 403 || !strings.Contains(w.Body.String(), "IP_BLOCKED") {
		t.Fatalf("scoped peer bypass %d %s", w.Code, w.Body.String())
	}
	// Self-lockout uses the same address normalization, never a raw ClientIP.
	if w := blocklistCall(a, token, "POST", blocklistRoute, `{"network":"fd00::/64"}`, "[fd00::5%eth0]:9", ""); w.Code != 409 {
		t.Fatalf("scoped self lockout %d %s", w.Code, w.Body.String())
	}
	// Policy denial must run before body limits and before debug body capture.
	var logs, appLog bytes.Buffer
	r := gin.New()
	r.Use(requestDiagnostics(true, &logs, &appLog, a.blocklistedIP())...)
	called := false
	r.POST("/probe", func(c *gin.Context) { called = true; c.Status(200) })
	const secret = `{"note":"BLOCKED_BODY_SECRET"}`
	req := httptest.NewRequest("POST", "/probe", strings.NewReader(secret))
	req.ContentLength = int64(len(secret))
	req.RemoteAddr = "198.51.100.9:9999"
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != 403 || called {
		t.Fatalf("blocked body handled %d called=%v", out.Code, called)
	}
	if strings.Contains(logs.String(), "BLOCKED_BODY_SECRET") || appLog.Len() != 0 {
		t.Fatalf("blocked body logged: %s / %s", logs.String(), appLog.String())
	}
	// An oversized declaration from a blocked peer is a denial, not a 413.
	oversized := httptest.NewRequest("POST", "/probe", strings.NewReader("{}"))
	oversized.ContentLength = defaultRequestBodyLimit + 1
	oversized.RemoteAddr = "198.51.100.9:9999"
	out = httptest.NewRecorder()
	r.ServeHTTP(out, oversized)
	if out.Code != 403 || called {
		t.Fatalf("blocked oversized request %d", out.Code)
	}
	// An unblocked peer still gets the ordinary body budget enforcement.
	allowed := httptest.NewRequest("POST", "/probe", strings.NewReader("{}"))
	allowed.ContentLength = defaultRequestBodyLimit + 1
	allowed.RemoteAddr = "203.0.113.5:9999"
	out = httptest.NewRecorder()
	r.ServeHTTP(out, allowed)
	if out.Code != 413 {
		t.Fatalf("unblocked oversized request %d", out.Code)
	}
}

func TestIPBlocklistMalformedConfigFailsStartup(t *testing.T) {
	a := contractTestApp(t)
	cfg := a.cfg
	a.Close()
	database, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetKV("security:ip-blocklist:v1", ""); err != nil {
		t.Fatal(err)
	}
	database.Close()
	// A stored empty value is corrupt, not "no rules": the server must refuse to
	// start instead of silently serving with an open policy.
	if _, err := newApp(cfg, cache.NewMemory(16)); err == nil {
		t.Fatal("empty persisted policy started allow-all")
	}
}
