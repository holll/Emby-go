package server

import (
	"encoding/json"
	"fmt"
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

func TestAuthRateLimitBlocksAccount(t *testing.T) {
	a := limitsInitializedApp(t)
	for i := 0; i < authAccountLimit; i++ {
		w := limitsLogin(a, "admin", "wrong-password")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	w := limitsLogin(a, "admin", "wrong-password")
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
	// 封禁期内即使密码正确也不放行：否则攻击者猜中后一次成功即可绕过计数。
	if w := limitsLogin(a, "admin", "password-1234"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password while blocked: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthSuccessResetsFailureBudget(t *testing.T) {
	a := limitsInitializedApp(t)
	for i := 0; i < authAccountLimit-1; i++ {
		if w := limitsLogin(a, "admin", "wrong-password"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status=%d", i+1, w.Code)
		}
	}
	if w := limitsLogin(a, "admin", "password-1234"); w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	for i := 0; i < authAccountLimit-1; i++ {
		if w := limitsLogin(a, "admin", "wrong-password"); w.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
}

func TestAuthRateLimitCountsPerIPAcrossAccounts(t *testing.T) {
	a := limitsInitializedApp(t)
	for i := 0; i < authIPLimit; i++ {
		w := limitsLogin(a, fmt.Sprintf("spray-%d", i), "wrong-password")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	if w := limitsLogin(a, "spray-final", "wrong-password"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInitializeRateLimited(t *testing.T) {
	a := limitsTestApp(t)
	for i := 0; i < authIPLimit; i++ {
		w := limitsPost(a.Handler(), "/api/auth/initialize", `{"Username":"ab","Pw":"short"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	w := limitsPost(a.Handler(), "/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if retry := w.Header().Get("Retry-After"); retry == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestAuthLimiterWindowExpiry(t *testing.T) {
	l := newAuthRateLimiter()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < authAccountLimit; i++ {
		l.recordFailure("198.51.100.7", "Admin")
	}
	if got := l.retryAfter("198.51.100.7", "admin"); got <= 0 || got > authAccountWindow {
		t.Fatalf("retryAfter=%v", got)
	}
	now = now.Add(authAccountWindow + time.Second)
	if got := l.retryAfter("198.51.100.7", "admin"); got != 0 {
		t.Fatalf("expired window retryAfter=%v", got)
	}
	// 窗口过期后重新计数：一次失败不应再次封禁。
	l.recordFailure("198.51.100.7", "admin")
	if got := l.retryAfter("198.51.100.7", "admin"); got != 0 {
		t.Fatalf("after reset retryAfter=%v", got)
	}
}
