package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/config"
)

func deletionEvent() Event {
	return Event{Event: ItemDeleted, Item: Item{Id: "movie-id", Name: "Movie", Path: "/strm/Movie/Movie.strm", Type: "Movie"}, Server: Server{Id: "server-id", Name: "Emby-go"}, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func senderFor(t *testing.T, endpoint string) *Sender {
	t.Helper()
	s, err := New(config.WebhookConfig{Enabled: true, URL: endpoint, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestSendSuccess(t *testing.T) {
	var count atomic.Int32
	want := deletionEvent()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected JSON POST")
		}
		if !r.Close {
			t.Error("request must not leave outbound idle connections")
		}
		var got Event
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		expected := want
		expected.Date = want.Timestamp
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("wrong event: %+v", got)
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	s := senderFor(t, srv.URL+"/webhook/secret-token")
	if err := s.Send(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatalf("requests=%d", count.Load())
	}
	s.Close()
	s.Close()
}

func TestDisabled(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer srv.Close()
	s, err := New(config.WebhookConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if s.client != nil {
		t.Fatal("disabled must not construct client")
	}
	if err := s.Send(nil, Event{}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var empty *Sender
	empty.Close()
	if err := empty.Send(nil, Event{}); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 0 {
		t.Fatal("disabled sender transmitted request")
	}
}

func TestSendResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		sentinel error
	}{
		{"empty ack", 204, "", nil},
		{"accepted ack", 202, "accepted", nil},
		{"ignored", 200, "ignored", ErrIgnored},
		{"disabled receiver", 200, "ignored: emby 删除同步未启用", ErrIgnored},
		{"case insensitive", 200, "IGNORED", ErrIgnored},
		{"localized", 200, "删除同步未启用", ErrIgnored},
		{"large", 200, strings.Repeat("x", maxResponseBytes*2), ErrResponseTooLarge},
		{"exact limit conservative", 200, strings.Repeat("x", maxResponseBytes), ErrResponseTooLarge},
		{"bad request", 400, "secret-response", nil},
		{"forbidden", 403, "secret-response", nil},
		{"server failure", 500, "secret-response", nil},
		{"redirect without location", 302, "secret-response", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			err := senderFor(t, srv.URL+"/secret-token").Send(context.Background(), deletionEvent())
			if tc.status < 200 || tc.status >= 300 {
				var status *StatusError
				if !errors.As(err, &status) || status.StatusCode != tc.status {
					t.Fatalf("expected status %d, got %v", tc.status, err)
				}
			} else if !errors.Is(err, tc.sentinel) {
				t.Fatalf("error=%v want=%v", err, tc.sentinel)
			}
			if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), srv.URL)) {
				t.Fatal("error leaked URL or body")
			}
			if count.Load() != 1 {
				t.Fatalf("requests=%d", count.Load())
			}
		})
	}
}

func TestRedirectNeverFollowed(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var originCount, targetCount atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCount.Add(1) }))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCount.Add(1)
				w.Header().Set("Location", target.URL+"/secret-target-token")
				w.WriteHeader(status)
			}))
			defer origin.Close()
			err := senderFor(t, origin.URL+"/secret-origin-token").Send(context.Background(), deletionEvent())
			var statusErr *StatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != status {
				t.Fatalf("unexpected %v", err)
			}
			if targetCount.Load() != 0 || originCount.Load() != 1 {
				t.Fatal("redirect followed or request retried")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "http") {
				t.Fatal("URL leaked")
			}
		})
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	for _, bodyRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("bodyRead=%v", bodyRead), func(t *testing.T) {
			var count atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				io.Copy(io.Discard, r.Body)
				if bodyRead {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			s := senderFor(t, srv.URL+"/secret-token")
			s.client.Timeout = 50 * time.Millisecond
			start := time.Now()
			err := s.Send(context.Background(), deletionEvent())
			if err == nil {
				t.Fatal("expected client total timeout")
			}
			if time.Since(start) > time.Second {
				t.Fatal("timeout did not bound response read")
			}
			if count.Load() != 1 {
				t.Fatalf("requests=%d", count.Load())
			}
			if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), srv.URL) {
				t.Fatal("error leaked URL")
			}
		})
	}
	t.Run("caller context during body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := senderFor(t, srv.URL).Send(ctx, deletionEvent())
		if !errors.Is(err, ErrReadResponse) || time.Since(start) > time.Second {
			t.Fatalf("context did not stop body read: %v", err)
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		var count atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := senderFor(t, srv.URL).Send(ctx, deletionEvent()); !errors.Is(err, ErrSend) {
			t.Fatalf("got %v", err)
		}
		if count.Load() != 0 {
			t.Fatal("cancelled request reached server")
		}
	})
}

func TestNewConfigurationAndPrivatePolicy(t *testing.T) {
	for _, cfg := range []config.WebhookConfig{
		{Enabled: true}, {Enabled: true, URL: "file:///secret-token"},
		{Enabled: true, URL: "http://user:secret-token@example.com"},
		{Enabled: true, URL: "https://example.com/#secret-token"},
		{Enabled: true, URL: "http://127.0.0.1/secret-token"},
		{TimeoutSeconds: -1}, {TimeoutSeconds: 31},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatal("expected invalid config")
		}
	}
	s, err := New(config.WebhookConfig{Enabled: true, URL: "https://receiver.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.client.Timeout != 5*time.Second {
		t.Fatal("default timeout")
	}
	// Bypass only offline validation internally to exercise outbound at send time.
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer srv.Close()
	s.url = srv.URL + "/secret-token"
	if err := s.Send(context.Background(), deletionEvent()); !errors.Is(err, ErrSend) {
		t.Fatalf("private policy error=%v", err)
	}
	if count.Load() != 0 {
		t.Fatal("private request escaped outbound policy")
	}
}

func TestInvalidEvent(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer srv.Close()
	s := senderFor(t, srv.URL)
	for _, event := range []Event{{}, {Event: "library.deleted", Item: Item{Path: "/movie.strm"}}, {Event: ItemDeleted}} {
		if err := s.Send(context.Background(), event); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("got %v", err)
		}
	}
	event := deletionEvent()
	event.Timestamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Send(context.Background(), event); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid timestamp: %v", err)
	}
	if err := s.Send(nil, deletionEvent()); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("nil ctx: %v", err)
	}
	if count.Load() != 0 {
		t.Fatal("invalid event transmitted")
	}
}
