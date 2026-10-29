package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func limitsTestApp(t *testing.T) *App {
	t.Helper()
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "limits.db")}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func limitsPost(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func limitsLogin(a *App, username, password string) *httptest.ResponseRecorder {
	return limitsPost(a.Handler(), "/Users/AuthenticateByName",
		`{"Username":"`+username+`","Pw":"`+password+`"}`)
}

func limitsInitializedApp(t *testing.T) *App {
	t.Helper()
	a := limitsTestApp(t)
	if err := a.db.InitializeAdministrator("admin", "password-1234"); err != nil {
		t.Fatal(err)
	}
	return a
}

func decodeLimitsBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return body
}

func TestBodyLimitForPaths(t *testing.T) {
	cases := []struct {
		path string
		want int64
	}{
		{"/api/auth/initialize", defaultRequestBodyLimit},
		{"/api/admin/items/42/images/poster", imageUploadBodyLimit},
		{"/api/admin/libraries/3/cover", imageUploadBodyLimit},
		// 只有 /images/ 段的影片图片上传放宽；其它管理端点仍是默认预算。
		{"/api/admin/items/42", defaultRequestBodyLimit},
		{"/api/admin/libraries/3", defaultRequestBodyLimit},
	}
	for _, tc := range cases {
		if got := bodyLimitFor(tc.path); got != tc.want {
			t.Errorf("bodyLimitFor(%q)=%d want %d", tc.path, got, tc.want)
		}
	}
}

func TestRequestBodyLimitRejectsOversizedJSON(t *testing.T) {
	a := limitsTestApp(t)
	huge := `{"Username":"` + strings.Repeat("a", int(defaultRequestBodyLimit)) + `"}`
	w := limitsPost(a.Handler(), "/api/auth/initialize", huge)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := decodeLimitsBody(t, w)
	if body["code"] != "BODY_TOO_LARGE" {
		t.Fatalf("body=%v", body)
	}
}

func TestImageUploadGetsLargerBodyBudget(t *testing.T) {
	a := limitsTestApp(t)
	// 2 MiB：超过默认预算、低于图片上传预算。未带凭据时应走到鉴权（401），
	// 而不是在体积中间件被 413 拦掉。
	payload := `{"file":"` + strings.Repeat("a", 2<<20) + `"}`
	w := limitsPost(a.Handler(), "/api/admin/items/1/images/poster", payload)
	if w.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("image upload rejected by default budget: %s", w.Body.String())
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func mustAdmit(t *testing.T, g *authGuard, ip string) *authTicket {
	t.Helper()
	ticket, _ := g.AdmitRequest(ip)
	if ticket == nil {
		t.Fatalf("AdmitRequest(%s) unexpectedly rejected", ip)
	}
	return ticket
}

func mustAttach(t *testing.T, g *authGuard, ticket *authTicket, username string) {
	t.Helper()
	if retry := g.AttachAccount(ticket, username); retry > 0 {
		t.Fatalf("AttachAccount(%s) rejected with %v", username, retry)
	}
}

func TestAuthGuardComboBlocksRepeatedGuesses(t *testing.T) {
	g := newAuthGuard(authGuardConfig{})
	const ip = "198.51.100.7"
	for range authComboFailureLimit {
		ticket := mustAdmit(t, g, ip)
		mustAttach(t, g, ticket, "admin")
		g.Finish(ticket, authOutcomeInvalidPassword)
	}
	ticket := mustAdmit(t, g, ip)
	if retry := g.AttachAccount(ticket, "admin"); retry <= 0 {
		t.Fatal("组合失败阈值未生效：第 6 次仍可尝试验证")
	}
	g.Finish(ticket, authOutcomeInterrupted)
}

func TestAuthGuardSuccessClearsAccountAndComboOnly(t *testing.T) {
	g := newAuthGuard(authGuardConfig{})
	const ip = "198.51.100.7"
	for range 2 {
		ticket := mustAdmit(t, g, ip)
		mustAttach(t, g, ticket, "admin")
		g.Finish(ticket, authOutcomeInvalidPassword)
	}
	g.OnCredentialCommitSuccess(ip, "ADMIN")
	g.mu.Lock()
	ipLeft := g.failures[authIPKey(ip)].failures
	_, accountLeft := g.failures[authAccountDigest("admin")]
	total := len(g.failures)
	g.mu.Unlock()
	if ipLeft != 2 {
		t.Fatalf("成功登录不得清除 IP 喷洒历史：left=%d", ipLeft)
	}
	if accountLeft || total != 1 {
		t.Fatalf("账号/组合失败历史未清理：accountLeft=%v total=%d", accountLeft, total)
	}
}

func TestAuthGuardAccountCooldownIsBounded(t *testing.T) {
	g := newAuthGuard(authGuardConfig{AccountCooldownMaxSeconds: 300})
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	// 账号维度要跨来源累积：同一 IP/账号组合会在 5 次后被组合阈值先挡住。
	for i := range authAccountFailureLimit {
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		ticket := mustAdmit(t, g, ip)
		mustAttach(t, g, ticket, "admin")
		g.Finish(ticket, authOutcomeInvalidPassword)
		now = now.Add(time.Second)
	}
	const lastIP = "203.0.113.9"
	ticket := mustAdmit(t, g, lastIP)
	retry := g.AttachAccount(ticket, "admin")
	g.Finish(ticket, authOutcomeInterrupted)
	if retry <= 0 || retry > 5*time.Minute+time.Second {
		t.Fatalf("账号冷却 retry=%v 超出 5 分钟上限", retry)
	}
	now = now.Add(5*time.Minute + time.Second)
	ticket = mustAdmit(t, g, lastIP)
	if retry := g.AttachAccount(ticket, "admin"); retry > 0 {
		t.Fatalf("有界冷却到期后仍被永久锁定：retry=%v", retry)
	}
	g.Finish(ticket, authOutcomeInterrupted)
}

func TestAuthGuardInflightReservationsShareBudget(t *testing.T) {
	g := newAuthGuard(authGuardConfig{})
	const ip = "198.51.100.7"
	tickets := make([]*authTicket, 0, authComboFailureLimit)
	for range authComboFailureLimit {
		ticket := mustAdmit(t, g, ip)
		mustAttach(t, g, ticket, "admin")
		tickets = append(tickets, ticket)
	}
	ticket := mustAdmit(t, g, ip)
	if retry := g.AttachAccount(ticket, "admin"); retry <= 0 {
		t.Fatal("在途预留未被计入阈值，并发请求可穿透")
	}
	for _, t2 := range append(tickets, ticket) {
		g.Finish(t2, authOutcomeInterrupted)
	}
}

func TestAuthGuardIPFailureSprayAcrossAccounts(t *testing.T) {
	g := newAuthGuard(authGuardConfig{})
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	const ip = "198.51.100.7"
	for i := range authIPFailureLimit {
		ticket := mustAdmit(t, g, ip)
		if retry := g.AttachAccount(ticket, fmt.Sprintf("spray-%d", i)); retry > 0 {
			t.Fatalf("第 %d 次在阈值前被拒绝：%v", i+1, retry)
		}
		g.Finish(ticket, authOutcomeInvalidPassword)
		now = now.Add(time.Second)
	}
	// IP 喷洒阈值在入场处生效：不需要再解析账号就应拒绝。
	if ticket, retry := g.AdmitRequest(ip); ticket != nil || retry <= 0 {
		t.Fatalf("IP 喷洒阈值未生效：ticket=%v retry=%v", ticket, retry)
	}
}

func TestAuthGuardIPRequestBurst(t *testing.T) {
	g := newAuthGuard(authGuardConfig{})
	const ip = "198.51.100.7"
	for range defaultAuthIPRequestBurst {
		ticket := mustAdmit(t, g, ip)
		g.Finish(ticket, authOutcomeInterrupted)
	}
	if ticket, retry := g.AdmitRequest(ip); ticket != nil || retry <= 0 {
		t.Fatalf("入场桶未限制突发：ticket=%v retry=%v", ticket, retry)
	}
}

func TestAuthSuccessClearsAccountBudgetButNotIPSpray(t *testing.T) {
	a := limitsInitializedApp(t)
	for i := range authComboFailureLimit - 1 {
		if w := limitsLogin(a, "admin", "wrong-password"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	if w := limitsLogin(a, "admin", "password-1234"); w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	// 成功登录清掉账号+组合失败；IP 桶里的请求令牌与失败喷洒历史不清。
	if w := limitsLogin(a, "admin", "wrong-password"); w.Code != http.StatusUnauthorized {
		t.Fatalf("success falsely reset IP history: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthRateLimitedResponseShape(t *testing.T) {
	a := limitsInitializedApp(t)
	for i := range authComboFailureLimit {
		if w := limitsLogin(a, "admin", "wrong-password"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	w := limitsLogin(a, "admin", "password-1234")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if retry := w.Header().Get("Retry-After"); retry == "" || retry == "0" {
		t.Fatalf("Retry-After=%q", retry)
	}
	body := decodeLimitsBody(t, w)
	if body["code"] != "AUTH_RATE_LIMITED" || body["retryable"] != true {
		t.Fatalf("body=%v", body)
	}
}

func TestCredentialEndpointRejectsOversizedChunkedBody(t *testing.T) {
	// 没有 Content-Length 的 chunked 请求也必须在上限处返回 413，而不是
	// 让 JSON 解码器读到半截后变成含糊的 400。
	a := limitsInitializedApp(t)
	payload := `{"Username":"admin","Pw":"` + strings.Repeat("a", 9000) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", nil)
	req.Body = io.NopCloser(strings.NewReader(payload))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if body := decodeLimitsBody(t, w); body["code"] != "AUTH_BODY_TOO_LARGE" {
		t.Fatalf("body=%v", body)
	}
}

func TestCredentialEndpointRejectsOversizedBody(t *testing.T) {
	a := limitsInitializedApp(t)
	payload := `{"Username":"admin","Pw":"` + strings.Repeat("a", 9000) + `"}`
	w := limitsPost(a.Handler(), "/Users/AuthenticateByName", payload)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := decodeLimitsBody(t, w)
	if body["code"] != "AUTH_BODY_TOO_LARGE" {
		t.Fatalf("body=%v", body)
	}
}
