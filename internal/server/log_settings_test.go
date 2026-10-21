package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
)

func logSettingsCall(a *App, method, token, tag, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/logs/settings", strings.NewReader(body))
	r.Header.Set("X-Emby-Token", token)
	r.Header.Set("If-Match", tag)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func TestLogSettingsAuthorizationValidationAndPersistence(t *testing.T) {
	f := newAdminDeleteFixture(t)
	u, err := f.a.db.CreateUser("logs-reader", "test-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("logs-reader", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	calls := []int{}
	f.a.applyLogRetention = func(n int) error { calls = append(calls, n); return nil }
	for _, method := range []string{"GET", "PUT"} {
		for token, want := range map[string]int{"": 401, "logs-reader": 403} {
			if w := logSettingsCall(f.a, method, token, logRetentionETag(0), `{"retention_days":7,"confirm_cleanup":true}`); w.Code != want {
				t.Fatal(method, w.Code, w.Body.String())
			}
		}
	}
	if w := logSettingsCall(f.a, "GET", "fixture-admin-delete", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"retention_days":0`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := logSettingsCall(f.a, "PUT", "fixture-admin-delete", "", `{"retention_days":7,"confirm_cleanup":true}`); w.Code != 428 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, raw := range []string{`{}`, `null`, `{"retention_days":null}`, `{"retention_days":true}`, `{"retention_days":-1}`, `{"retention_days":3651}`, `{"retention_days":2.5}`, `{"retention_days":7}`, `{"retention_days":0,"unknown":1}`, `{"retention_days":0} {}`, strings.Repeat(" ", 1025) + `{}`} {
		if w := logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(0), raw); w.Code != 400 {
			t.Fatal(raw, w.Code, w.Body.String())
		}
	}
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	w := logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(0), `{"retention_days":7,"confirm_cleanup":true}`)
	if w.Code != 200 || len(calls) != 1 || calls[0] != 7 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	if w = logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(0), `{"retention_days":30,"confirm_cleanup":true}`); w.Code != 412 || len(calls) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.a.Close()
	reopened, err := newApp(f.a.cfg, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if w = logSettingsCall(reopened, "GET", "fixture-admin-delete", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"retention_days":7`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = logSettingsCall(reopened, "PUT", "fixture-admin-delete", logRetentionETag(7), `{"retention_days":0}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLogSettingsWriteFailureCleanupWarningAndConflict(t *testing.T) {
	f := newAdminDeleteFixture(t)
	calls := 0
	f.a.applyLogRetention = func(n int) error { calls++; return errors.New("private-path-sql-canary") }
	if _, err := f.db.Exec(`CREATE TRIGGER refuse_log_setting BEFORE INSERT ON settings WHEN NEW.key='logging.retention_days' BEGIN SELECT RAISE(ABORT,'private-sql-canary'); END`); err != nil {
		t.Fatal(err)
	}
	w := logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(0), `{"retention_days":7,"confirm_cleanup":true}`)
	if w.Code != 500 || calls != 0 || f.a.logRetentionDays != 0 || strings.Contains(w.Body.String(), "canary") {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	if _, err := f.db.Exec(`DROP TRIGGER refuse_log_setting`); err != nil {
		t.Fatal(err)
	}
	w = logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(0), `{"retention_days":7,"confirm_cleanup":true}`)
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), `"cleanup_warning":true`) || strings.Contains(w.Body.String(), "canary") {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, err := f.a.db.Setting(settingLogRetention)
	if err != nil || raw != "7" {
		t.Fatal(raw, err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, body := range []string{`{"retention_days":14,"confirm_cleanup":true}`, `{"retention_days":30,"confirm_cleanup":true}`} {
		wg.Add(1)
		go func(b string) {
			defer wg.Done()
			codes <- logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(7), b).Code
		}(body)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[412] != 1 || calls != 2 {
		t.Fatal(counts, calls)
	}
	f.a.BeginShutdown()
	if w = logSettingsCall(f.a, "PUT", "fixture-admin-delete", logRetentionETag(f.a.logRetentionDays), `{"retention_days":0}`); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLogSettingsCorruptStartupAndAsset(t *testing.T) {
	for _, raw := range []string{"", "oops", "-1", "3651"} {
		t.Run(raw, func(t *testing.T) {
			a, _, _ := authorityApp(t)
			if err := a.db.SetSetting(settingLogRetention, raw); err != nil {
				t.Fatal(err)
			}
			a.Close()
			app, err := newApp(a.cfg, cache.NewMemory(16))
			if err == nil {
				app.Close()
				t.Fatal("corrupt policy accepted")
			}
		})
	}
	f := newAdminDeleteFixture(t)
	r := httptest.NewRequest("GET", "/web/log-settings.js", nil)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "mountLogSettings") || w.Header().Get("ETag") == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/web/log-settings.js", nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]any
	w = logSettingsCall(f.a, "GET", "fixture-admin-delete", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
}
