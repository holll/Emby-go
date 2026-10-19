package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"github.com/gin-gonic/gin"
)

func TestDiagnosticQueryAllowlist(t *testing.T) {
	for _, query := range []string{
		"Password=canary-password&Signature=canary-signature", "canary-key=anything", "Limit=canary-number&Static=canary-bool",
		"Limit=10&Limit=canary-duplicate", "searchTerm=canary-search", "token=canary&bad=%ZZ", "x=" + strings.Repeat("canary", 1000),
	} {
		got := redactedRequestQuery(query)
		if strings.Contains(got, "canary") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("query leaked: %s", got)
		}
	}
	if got := redactedRequestQuery("Limit=10&Static=true&MediaSourceId=1"); got != "Limit=10&MediaSourceId=1&Static=true" {
		t.Fatalf("allowed scalar lost: %s", got)
	}
	for _, path := range []string{"/api/admin/apikeys/canary", "/API/ADMIN/APIKEYS/canary?Password=canary"} {
		if got := unmatchedDiagnosticPath(path); strings.Contains(got, "canary") {
			t.Fatalf("key path leaked: %s", got)
		}
	}
}

func TestDiagnosticRequestCopies(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	for _, status := range []int{200, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var output bytes.Buffer
			r := gin.New()
			r.Use(requestLoggerTo(&output), requestContentLoggerTo(&output), requestRecoveryTo(&output))
			const body = `{"Password":"canary-body"}`
			const target = "/api/admin/apikeys/canary-path?token=canary-query&Signature=canary-signature&canary-key=x&Limit=canary-number&Static=canary-bool"
			r.DELETE("/api/admin/apikeys/:key", func(c *gin.Context) {
				if c.Param("key") != "canary-path" || c.Request.RequestURI != target || c.GetHeader("Authorization") != "Bearer canary-auth" || c.GetHeader("Cookie") != "sid=canary-cookie" || c.GetHeader("X-Private") != "canary-header" {
					t.Error("logger changed request")
				}
				b, err := io.ReadAll(c.Request.Body)
				if err != nil || string(b) != body {
					t.Errorf("body changed: %s err=%v", b, err)
				}
				_ = c.Error(errors.New("canary-error"))
				c.Status(status)
			})
			req := httptest.NewRequest(http.MethodDelete, target, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer canary-auth")
			req.Header.Set("Cookie", "sid=canary-cookie")
			req.Header.Set("X-Private", "canary-header")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			got := output.String()
			if w.Code != status || strings.Contains(got, "canary") || !strings.Contains(got, "[API REQUEST]") || !strings.Contains(got, "/api/admin/apikeys/:key") || !strings.Contains(got, "error=true") {
				t.Fatalf("status=%d log=%s", w.Code, got)
			}
		})
	}
}

type diagnosticUnreadBody struct{ reads int }

func (b *diagnosticUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("canary-body-read-error")
}
func (b *diagnosticUnreadBody) Close() error { return nil }

func TestDiagnosticPanicAndUnreadBody(t *testing.T) {
	var output bytes.Buffer
	r := gin.New()
	r.Use(requestLoggerTo(&output), requestContentLoggerTo(&output), requestRecoveryTo(&output))
	r.POST("/Users/AuthenticateByName", func(c *gin.Context) { panic("canary-panic") })
	body := &diagnosticUnreadBody{}
	req := httptest.NewRequest("POST", "/Users/AuthenticateByName?token=canary-query", body)
	req.Header.Set("Authorization", "Bearer canary-header")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if body.reads != 0 || w.Code != 500 || strings.Contains(output.String(), "canary") || !strings.Contains(output.String(), "[PANIC]") {
		t.Fatalf("reads=%d status=%d log=%s", body.reads, w.Code, output.String())
	}
}

func TestDiagnosticNoRoutePersistence(t *testing.T) {
	a, _, path := authorityApp(t)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var output bytes.Buffer
	r := gin.New()
	r.Use(requestLoggerTo(&output), requestContentLoggerTo(&output))
	r.NoRoute(a.noRoute)
	for i := 0; i < 200; i++ {
		body := &diagnosticUnreadBody{}
		req := httptest.NewRequest("POST", fmt.Sprintf("/Users/canary-path-%d/Unimplemented?token=canary-query&canary-key=x&Limit=10", i), body)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 404 || body.reads != 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("404 status=%d reads=%d", w.Code, body.reads)
		}
	}
	rows, err := db.Query("SELECT method,path,query,body_preview FROM api_probe")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var method, path, query, body string
		if err := rows.Scan(&method, &path, &query, &body); err != nil {
			t.Fatal(err)
		}
		n++
		if method != "POST" || path != "/users/[UNMATCHED]" || body != "[OMITTED]" || strings.Contains(query, "canary") || !strings.Contains(query, "Limit=10") {
			t.Fatalf("persisted unsafe metadata %q %q %q %q", method, path, query, body)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(a.probeSeen) != 1 || strings.Contains(output.String(), "canary") {
		t.Fatalf("rows=%d seen=%d log contains canary=%t", n, len(a.probeSeen), strings.Contains(output.String(), "canary"))
	}
	// Even pre-filled legacy or synthetic keys cannot grow an already full map.
	for i := 0; len(a.probeSeen) < 128; i++ {
		a.probeSeen[fmt.Sprint(i)] = probeSeenEntry{seenAt: time.Now()}
	}
	req := httptest.NewRequest("GET", "/System/AnotherUnimplemented", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)
	if len(a.probeSeen) != 128 {
		t.Fatalf("map exceeded cap: %d", len(a.probeSeen))
	}
}

type lockedDiagnosticBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *lockedDiagnosticBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *lockedDiagnosticBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestDiagnosticAPIKeyBusinessEvents(t *testing.T) {
	var output lockedDiagnosticBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	a, _, path := authorityApp(t)
	seedOwner(t, a)
	// A short legacy fixture key must not be printed in full by prefix logging.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO api_keys(key,name,created_at) VALUES('canary','fixture','2026-10-17T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SaveAccessToken("fixture-log-auth"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	req := httptest.NewRequest("POST", "/api/admin/apikeys", strings.NewReader(`{"name":"canary-pasted-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", "fixture-log-auth")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var created struct {
		Key string `json:"key"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Key == "" {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	for _, key := range []string{created.Key, "canary"} {
		req := httptest.NewRequest("DELETE", "/api/admin/apikeys/"+key, nil)
		req.Header.Set("X-Emby-Token", "fixture-log-auth")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 204 {
			t.Fatalf("delete status=%d", w.Code)
		}
	}
	if valid, err := a.db.HasAPIKey(created.Key); err != nil || valid {
		t.Fatalf("delete did not revoke: %t %v", valid, err)
	}
	if got := output.String(); strings.Contains(got, "canary") || strings.Contains(got, created.Key[:min(8, len(created.Key))]) || !strings.Contains(got, "创建 API 密钥") || !strings.Contains(got, "删除 API 密钥") {
		t.Fatalf("business event leaked or missing: %s", got)
	}
}

func TestDebugUsesSafeFrameworkMode(t *testing.T) {
	a, err := newApp(config.Config{DBPath: t.TempDir() + "/debug.db", Debug: true}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if gin.Mode() != gin.ReleaseMode {
		t.Fatal("framework debug can print raw redirect URLs before middleware")
	}
	// Trailing-slash redirect remains enabled; only unsafe framework printing stops.
	req := httptest.NewRequest("POST", "/api/admin/apikeys/?token=canary-redirect", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect behavior changed: %d body=%s", w.Code, w.Body.String())
	}
}
