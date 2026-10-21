package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"emby-go/internal/config"
	"emby-go/internal/store"
	"emby-go/internal/webhook"
)

func enableDeleteWebhook(t *testing.T, f adminDeleteFixture, url string) {
	t.Helper()
	cfg := config.WebhookConfig{Enabled: true, URL: url, TimeoutSeconds: 1, AllowPrivate: true}
	sender, err := webhook.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.a.webhook.Close()
	f.a.webhook = sender
	f.a.cfg.Webhook = cfg
	t.Cleanup(sender.Close)
}

func TestDeleteWebhookNullTitleCompatibleWhenDisabledOrEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			if _, err := f.db.Exec("UPDATE movies SET title=NULL WHERE id=?", f.victim); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var event webhook.Event
				if err := json.NewDecoder(r.Body).Decode(&event); err != nil || event.Item.Name != "" {
					t.Error("null title payload", err)
				}
				if _, err := f.a.db.Movie(f.victim); !store.NotFound(err) {
					t.Error("event before commit", err)
				}
				w.Write([]byte("ok"))
			}))
			defer target.Close()
			if enabled {
				enableDeleteWebhook(t, f, target.URL)
			}
			w := adminDeleteRequest(f, "items", "fixture-admin-delete")
			want, count := "disabled", int32(0)
			if enabled {
				want, count = "sent", 1
			}
			if w.Code != 204 || w.Header().Get("X-Webhook-Status") != want || calls.Load() != count {
				t.Fatalf("null title status=%d header=%q calls=%d", w.Code, w.Header().Get("X-Webhook-Status"), calls.Load())
			}
		})
	}
}

func TestDeleteWebhookOnlyAfterCommit(t *testing.T) {
	for _, fault := range []string{"none", "global", "delete", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			original, err := f.a.db.Movie(f.victim)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			events := make(chan webhook.Event, 1)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid request contract")
				}
				var received webhook.Event
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				events <- received
				if _, err := f.a.db.Movie(f.victim); !store.NotFound(err) {
					t.Error("notification preceded committed deletion", err)
				}
				w.Write([]byte("ok"))
			}))
			defer target.Close()
			enableDeleteWebhook(t, f, target.URL+"/webhook/private-token")
			if w := adminDeleteRequest(f, "items", ""); w.Code != 401 {
				t.Fatal(w.Code)
			}
			trigger := ""
			switch fault {
			case "global":
				trigger = `CREATE TRIGGER webhook_fault BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'private-canary'); END`
			case "delete":
				trigger = `CREATE TRIGGER webhook_fault BEFORE DELETE ON movies BEGIN SELECT RAISE(ABORT,'private-canary'); END`
			case "commit":
				trigger = `CREATE TABLE webhook_parent(id INTEGER PRIMARY KEY); CREATE TABLE webhook_child(id INTEGER REFERENCES webhook_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER webhook_fault AFTER DELETE ON movies BEGIN INSERT INTO webhook_child VALUES(123); END`
			}
			before := adminDeleteSnapshot(t, f)
			if trigger != "" {
				if _, err := f.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
				if w := adminDeleteRequest(f, "items", "fixture-admin-delete"); w.Code != 500 {
					t.Fatalf("fault status=%d", w.Code)
				}
				if calls.Load() != 0 || before != adminDeleteSnapshot(t, f) {
					t.Fatal("failed commit sent an event or changed state")
				}
				if _, err := f.db.Exec("DROP TRIGGER webhook_fault"); err != nil {
					t.Fatal(err)
				}
			}
			w := adminDeleteRequest(f, "items", "fixture-admin-delete")
			if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("X-Webhook-Status") != "sent" {
				t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			var received webhook.Event
			select {
			case received = <-events:
			default:
				t.Fatal("missing delivered event")
			}
			if calls.Load() != 1 || received.Event != "item.deleted" || received.Item.Path != original.SourcePath || received.Item.Name != original.Title || received.Item.Id != fmt.Sprint(f.victim) || received.Item.Type != "Movie" || received.Server.Id != f.a.serverID || received.Timestamp.IsZero() {
				t.Fatalf("event mismatch: %+v calls=%d", received, calls.Load())
			}
			if w := adminDeleteRequest(f, "items", "fixture-admin-delete"); w.Code != 404 || calls.Load() != 1 {
				t.Fatal("repeated delete sent event")
			}
			for path, body := range f.files {
				b, err := os.ReadFile(path)
				if err != nil || string(b) != body {
					t.Fatalf("local source changed: %v", err)
				}
			}
		})
	}
}

func TestDeleteWebhookFailureDoesNotUndoDeletion(t *testing.T) {
	for _, mode := range []string{"status", "ignored", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "status" {
					w.WriteHeader(500)
					w.Write([]byte("private-response-canary"))
				} else {
					w.Write([]byte("ignored: emby 删除同步未启用"))
				}
			}))
			defer target.Close()
			enableDeleteWebhook(t, f, target.URL)
			if mode == "shutdown" {
				f.a.BeginShutdown()
			}
			w := adminDeleteRequest(f, "items", "fixture-admin-delete")
			if w.Code != 204 || w.Header().Get("X-Webhook-Status") != "failed" || w.Body.Len() != 0 {
				t.Fatalf("status=%d header=%v", w.Code, w.Header())
			}
			if _, err := f.a.db.Movie(f.victim); !store.NotFound(err) {
				t.Fatal("local deletion was not committed")
			}
			want := int32(1)
			if mode == "shutdown" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("replayed request: %d", calls.Load())
			}
		})
	}
}

func TestDeleteWebhookNeverForLibraryOrInventoryCleanup(t *testing.T) {
	for _, mode := range []string{"library", "scan"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer target.Close()
			enableDeleteWebhook(t, f, target.URL)
			if mode == "library" {
				if w := adminDeleteRequest(f, "libraries", "fixture-admin-delete"); w.Code != 204 {
					t.Fatal(w.Code)
				}
			} else if err := f.a.db.ReconcileMissingSources(f.lib, nil); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("bulk/automatic deletion emitted destructive event")
			}
		})
	}
}

func TestDeleteWebhookDisabledOrUnsafePath(t *testing.T) {
	for _, mode := range []string{"disabled", "relative", "directory", "url"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer target.Close()
			if mode != "disabled" {
				enableDeleteWebhook(t, f, target.URL)
			}
			path := map[string]string{"relative": "movie.strm", "directory": "/media/folder", "url": "https://media.test/movie.strm"}[mode]
			if path != "" {
				if _, err := f.db.Exec("UPDATE movies SET source_path=? WHERE id=?", path, f.victim); err != nil {
					t.Fatal(err)
				}
			}
			w := adminDeleteRequest(f, "items", "fixture-admin-delete")
			expected := "skipped"
			if mode == "disabled" {
				expected = "disabled"
			}
			if w.Code != 204 || w.Header().Get("X-Webhook-Status") != expected || calls.Load() != 0 {
				t.Fatalf("status=%d headers=%v calls=%d", w.Code, w.Header(), calls.Load())
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("leaked private response")
			}
		})
	}
}
