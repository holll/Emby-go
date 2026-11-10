package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func webhookTestCall(a *App, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/webhook/test", strings.NewReader(body))
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}
func webhookTestBody(url, format string, timeout int, private bool) string {
	b, _ := json.Marshal(map[string]any{"url": url, "format": format, "timeout_seconds": timeout, "allow_private": private})
	return string(b)
}
func decodeWebhookTest(t *testing.T, w *httptest.ResponseRecorder) webhookTestResponse {
	t.Helper()
	var got webhookTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if got.Status != "sent" && got.Status != "failed" {
		t.Fatal("invalid status", w.Body.String())
	}
	if got.Message == "" || got.DurationMS < 0 {
		t.Fatal("missing result fields", w.Body.String())
	}
	var fields map[string]any
	json.Unmarshal(w.Body.Bytes(), &fields)
	for _, key := range []string{"status", "duration_ms", "error_code", "message"} {
		if _, ok := fields[key]; !ok {
			t.Fatal("missing", key)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	return got
}

func TestAdminWebhookTestAuthorizationAndInvalidDraft(t *testing.T) {
	f := newAdminDeleteFixture(t)
	user, err := createFixtureUser(t, f.a, "probe-ordinary", "test-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveFixtureSession(t, f.a, "probe-ordinary-token", user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]int{"": 401, "probe-ordinary-token": 403} {
		if w := webhookTestCall(f.a, token, `{}`); w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{`, `{"url":"https://example.com"} {}`, `{"url":"https://example.com","unknown":true}`, `{"url":123}`, webhookTestBody("", "json", 1, true), webhookTestBody("https://user:credential-canary@example.com", "json", 1, true), webhookTestBody("ftp://example.com/credential-canary", "json", 1, true), webhookTestBody("https://example.com", "xml", 1, true), webhookTestBody("https://example.com", "json", 31, true), webhookTestBody("https://example.com", "json", -1, true), webhookTestBody("http://127.0.0.1/credential-canary", "json", 1, false), strings.Repeat("x", 8193)} {
		w := webhookTestCall(f.a, "fixture-admin-delete", body)
		if w.Code != 400 {
			t.Fatalf("invalid draft code=%d body=%s", w.Code, w.Body.String())
		}
		got := decodeWebhookTest(t, w)
		if got.ErrorCode != "invalid_draft" || strings.Contains(w.Body.String(), "credential-canary") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestAdminWebhookTestDraftOnlyAndDeliveryResults(t *testing.T) {
	f := newAdminDeleteFixture(t)
	before, err := f.a.db.Settings()
	if err != nil {
		t.Fatal(err)
	}
	savedBefore := webhookSettingsCall(f.a, "GET", "fixture-admin-delete", "", "").Body.String()
	var calls atomic.Int32
	for _, format := range []string{"json", "form"} {
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var raw []byte
			if format == "form" {
				r.ParseForm()
				raw = []byte(r.Form.Get("data"))
			} else {
				raw, _ = io.ReadAll(r.Body)
			}
			var p map[string]any
			if json.Unmarshal(raw, &p) != nil || p["Event"] != "webhook.test" || p["Message"] == nil || p["Server"] == nil || p["Timestamp"] == nil {
				t.Errorf("bad probe: %s", raw)
			}
			for _, key := range []string{"Item", "User", "Session", "PlaybackInfo"} {
				if p[key] != nil {
					t.Error("business data", key)
				}
			}
			if strings.Contains(string(raw), "item.deleted") || strings.Contains(string(raw), ".strm") {
				t.Error("business event", string(raw))
			}
			io.WriteString(w, "ok")
		}))
		w := webhookTestCall(f.a, "fixture-admin-delete", webhookTestBody(receiver.URL+"/url-credential-canary?token=query-credential-canary", format, 1, true))
		receiver.Close()
		got := decodeWebhookTest(t, w)
		if w.Code != 200 || got.Status != "sent" || got.ErrorCode != "" {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "credential-canary") {
			t.Fatal("leaked URL", w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected sends", calls.Load())
	}
	for _, tc := range []struct {
		name, code string
		status     int
		body       string
		delay      time.Duration
	}{
		{"status", "status_503", 503, "response-credential-canary", 0}, {"ignored", "ignored", 200, "ignored response-credential-canary", 0},
		{"large", "response_too_large", 200, strings.Repeat("x", 4096), 0}, {"timeout", "timeout", 200, "", 1100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(tc.delay)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer receiver.Close()
			w := webhookTestCall(f.a, "fixture-admin-delete", webhookTestBody(receiver.URL+"/url-credential-canary?token=query-credential-canary", "json", 1, true))
			got := decodeWebhookTest(t, w)
			if w.Code != 200 || got.Status != "failed" || got.ErrorCode != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "credential-canary") {
				t.Fatal("credential leak", w.Body.String())
			}
		})
	}
	after, err := f.a.db.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("test mutated persistent settings", fmt.Sprint(before), fmt.Sprint(after))
	}
	if savedAfter := webhookSettingsCall(f.a, "GET", "fixture-admin-delete", "", "").Body.String(); savedAfter != savedBefore {
		t.Fatal("test changed active config")
	}
}

func TestAdminWebhookTestRequestCancellation(t *testing.T) {
	f := newAdminDeleteFixture(t)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer receiver.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/webhook/test", strings.NewReader(webhookTestBody(receiver.URL, "json", 30, true))).WithContext(ctx)
	req.Header.Set("X-Emby-Token", "fixture-admin-delete")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, req)
	if got := decodeWebhookTest(t, w); w.Code != 200 || got.ErrorCode != "canceled" || got.Status != "failed" {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("canceled request delivered")
	}
}

func TestAdminWebhookTestBusyNonblocking(t *testing.T) {
	f := newAdminDeleteFixture(t)
	for i := 0; i < cap(webhookTestSlots); i++ {
		webhookTestSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(webhookTestSlots); i++ {
			<-webhookTestSlots
		}
	}()
	w := webhookTestCall(f.a, "fixture-admin-delete", webhookTestBody("https://example.com", "json", 1, false))
	if got := decodeWebhookTest(t, w); w.Code != 200 || got.ErrorCode != "busy" || got.Status != "failed" {
		t.Fatal(w.Code, w.Body.String())
	}
}
