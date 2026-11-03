package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/config"
)

func TestSendTestPayloadFormats(t *testing.T) {
	for _, format := range []string{"json", "form"} {
		t.Run(format, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" {
					t.Error(r.Method)
				}
				var raw []byte
				if format == "form" {
					if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Error("wrong form content type")
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					raw = []byte(r.Form.Get("data"))
				} else {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("wrong json content type")
					}
					raw, _ = io.ReadAll(r.Body)
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Error(err)
				}
				if string(payload["Event"]) != `"webhook.test"` || len(payload["Message"]) == 0 || len(payload["Server"]) == 0 || len(payload["Timestamp"]) == 0 || len(payload["Date"]) == 0 {
					t.Errorf("missing test fields: %s", raw)
				}
				for _, key := range []string{"Item", "User", "Session", "PlaybackInfo"} {
					if _, exists := payload[key]; exists {
						t.Error("business data in probe", key)
					}
				}
				if strings.Contains(string(raw), ItemDeleted) || strings.Contains(string(raw), ".strm") {
					t.Error("unsafe probe", string(raw))
				}
				io.WriteString(w, "ok")
			}))
			defer srv.Close()
			s, err := New(config.WebhookConfig{Enabled: true, URL: srv.URL, Format: format, AllowPrivate: true, Events: []string{ItemUpdated}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.Subscribed(TestEvent) || config.ValidWebhookEvent(TestEvent) {
				t.Fatal("test must never be subscribable")
			}
			if err := s.SendTest(context.Background(), Server{Id: "test-server", Name: "Emby-go"}); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatal("must send exactly once", calls.Load())
			}
		})
	}
}

func TestSendTestAcknowledgmentsAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"ok", "ok", "", 204}, {"ignored", "ignored response-credential-canary", "ignored", 200},
		{"disabled", "未启用 response-credential-canary", "ignored", 200},
		{"status", "response-credential-canary", "status_500", 500},
		{"large", strings.Repeat("x", maxResponseBytes), "response_too_large", 200},
		{"redirect", "response-credential-canary", "status_302", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			s := senderFor(t, srv.URL+"/url-credential-canary?token=query-credential-canary")
			err := s.SendTest(context.Background(), Server{})
			if got := DeliveryErrorCode(err); got != tc.code {
				t.Fatalf("code=%s want=%s err=%v", got, tc.code, err)
			}
			if err != nil && strings.Contains(err.Error(), "credential-canary") {
				t.Fatal("credential leak", err)
			}
			if calls.Load() != 1 {
				t.Fatal("retry or redirect", calls.Load())
			}
		})
	}
}

func TestSendTestCancellationAndTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer srv.Close()
	s := senderFor(t, srv.URL+"/secret-canary")
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		want := "timeout"
		if canceled {
			cancel()
			want = "canceled"
		}
		err := s.SendTest(ctx, Server{})
		cancel()
		if DeliveryErrorCode(err) != want {
			t.Fatalf("want=%s err=%v", want, err)
		}
	}
	if !errors.Is(s.SendTest(nil, Server{}), ErrInvalidEvent) {
		t.Fatal("nil context accepted")
	}
	if !errors.Is((*Sender)(nil).SendTest(context.Background(), Server{}), ErrInvalidEvent) {
		t.Fatal("disabled sender accepted")
	}
}
