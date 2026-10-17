package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"emby-go/internal/metatube"
)

type healthUnprintableError struct{}

func (healthUnprintableError) Error() string { panic("raw error must never be formatted") }

func TestBackendHealthLogFieldsExcludeRawErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason string
		status int
	}{
		{"opaque", healthUnprintableError{}, "request_failed", 0},
		{"url", &url.Error{Op: "startup-op-canary", URL: "http://user-canary:pass-canary@host/path-canary?key=query-canary", Err: errors.New("proxy-canary")}, "request_failed", 0},
		{"canceled", fmt.Errorf("cancel-canary: %w", context.Canceled), "canceled", 0},
		{"deadline", &url.Error{Op: "startup-op-canary", URL: "http://query-canary", Err: context.DeadlineExceeded}, "timeout", 0},
		{"status", fmt.Errorf("prefix-canary: %w", &metatube.APIError{StatusCode: 503, Code: 42, Message: "remote-canary"}), "http_status", 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			slog.New(slog.NewJSONHandler(&b, nil)).Warn("health", backendHealthLogFields(tc.err)...)
			if strings.Contains(b.String(), "canary") {
				t.Fatalf("secret leaked: %s", b.String())
			}
			var record map[string]any
			if err := json.Unmarshal(b.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["reason"] != tc.reason {
				t.Fatalf("reason %v", record)
			}
			if tc.status != 0 {
				if record["status"] != float64(tc.status) {
					t.Fatalf("status %v", record)
				}
			} else if _, ok := record["status"]; ok {
				t.Fatalf("spurious status %v", record)
			}
			for _, key := range []string{"url", "error", "token", "message"} {
				if _, ok := record[key]; ok {
					t.Fatalf("unexpected field %s", key)
				}
			}
		})
	}
}

func TestStartupBackendHealthLoggingIsPrivateAndRequestCompatible(t *testing.T) {
	a, _, _ := authorityApp(t)
	a.startupWG.Wait()
	originalContext := a.rootCtx
	defer func() { a.rootCtx = originalContext }()
	type requestInfo struct{ method, path, query, user, password, authorization string }
	seen := make(chan requestInfo, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		seen <- requestInfo{r.Method, r.URL.Path, r.URL.Query().Get("key"), user, password, r.Header.Get("Authorization")}
		if strings.Contains(r.URL.Path, "fail") {
			// A peer controls the status text too; it must not be copied into logs.
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 503 startup-remote-canary\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			_ = rw.Flush()
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	target.User = url.UserPassword("startup-user-canary", "startup-password-canary")
	target.RawQuery = "key=startup-query-canary"
	target.Fragment = "startup-fragment-canary"
	if err := a.db.SetSetting(settingMetatubeToken, "startup-token-canary"); err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	defer slog.SetDefault(previous)
	for _, mode := range []string{"healthy", "http", "invalid", "unsupported", "canceled", "deadline", "blank"} {
		t.Run(mode, func(t *testing.T) {
			a.rootCtx = originalContext
			target.Path = "/startup-path-canary"
			wantReason := ""
			wantLevel := "WARN"
			wantStatus := 0
			wantRequest := false
			switch mode {
			case "healthy":
				wantRequest = true
				wantLevel = "INFO"
			case "http":
				target.Path = "/startup-fail-path-canary"
				wantRequest = true
				wantReason = "http_status"
				wantStatus = 503
			case "invalid":
				wantReason = "request_failed"
			case "unsupported":
				wantReason = "request_failed"
			case "canceled":
				ctx, cancel := context.WithCancel(originalContext)
				cancel()
				a.rootCtx = ctx
				wantReason = "canceled"
			case "deadline":
				ctx, cancel := context.WithDeadline(originalContext, time.Now().Add(-time.Second))
				defer cancel()
				a.rootCtx = ctx
				wantReason = "timeout"
			}
			base := target.String()
			if mode == "invalid" {
				base = "http://%startup-invalid-canary"
			}
			if mode == "unsupported" {
				base = "startup-scheme://startup-host-canary/path"
			}
			if mode == "blank" {
				base = ""
			}
			if err := a.db.SetSetting(settingMetatubeURL, base); err != nil {
				t.Fatal(err)
			}
			var output lockedDiagnosticBuffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			a.checkScrapeBackend()
			text := output.String()
			if strings.Contains(text, "canary") || strings.Contains(text, srv.URL) {
				t.Fatalf("sensitive startup log: %s", text)
			}
			if mode == "blank" {
				if text != "" {
					t.Fatalf("blank configuration logged: %s", text)
				}
			} else {
				var record map[string]any
				if err := json.Unmarshal([]byte(text), &record); err != nil {
					t.Fatalf("one event expected: %v %s", err, text)
				}
				if record["level"] != wantLevel {
					t.Fatalf("level %v", record)
				}
				if wantReason != "" {
					if record["reason"] != wantReason {
						t.Fatalf("reason %v", record)
					}
				} else if _, ok := record["reason"]; ok {
					t.Fatalf("healthy failure field: %v", record)
				}
				if wantStatus != 0 {
					if record["status"] != float64(wantStatus) {
						t.Fatalf("status %v", record)
					}
				} else if _, ok := record["status"]; ok {
					t.Fatalf("unexpected status %v", record)
				}
				for _, key := range []string{"url", "error", "token"} {
					if _, ok := record[key]; ok {
						t.Fatalf("raw field %s: %v", key, record)
					}
				}
			}
			if wantRequest {
				select {
				case req := <-seen:
					if req.method != "GET" || req.path != target.Path || req.query != "startup-query-canary" || req.user != "startup-user-canary" || req.password != "startup-password-canary" || !strings.HasPrefix(req.authorization, "Basic ") {
						t.Fatalf("request changed: %+v", req)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("local health request missing")
				}
			} else {
				select {
				case req := <-seen:
					t.Fatalf("unexpected request %+v", req)
				default:
				}
			}
		})
	}
}
