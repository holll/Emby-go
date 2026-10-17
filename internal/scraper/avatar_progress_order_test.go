package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
)

func concurrentAvatarFixture(t *testing.T, total, partial int) (*Scraper, *store.Store, []string) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	lib, err := db.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, total)
	for i := range names {
		names[i] = fmt.Sprintf("Actor %03d", i)
		if i >= partial {
			continue
		}
		source := filepath.Join(root, fmt.Sprintf("ABC-%03d.strm", i))
		nfo := source + ".nfo"
		if err := os.Mkdir(nfo, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: source, NFOPath: nfo, Status: "success"}, 1, time.Unix(1, 0), []store.ActorRef{{Name: names[i]}}); err != nil {
			t.Fatal(err)
		}
	}
	picture := avatarPNG(t, 1)
	var backend *httptest.Server
	backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/picture.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(picture)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "one", "name": r.URL.Query().Get("q"), "provider": "fixture", "images": []string{backend.URL + "/picture.png"}}}})
	}))
	t.Cleanup(backend.Close)
	return New(Config{BaseURL: backend.URL, Token: "fixture", Concurrency: 8, AvatarsDir: filepath.Join(root, "avatars")}, db), db, names
}

func TestAvatarConcurrentProgressDeliveryIsOrdered(t *testing.T) {
	s, db, names := concurrentAvatarFixture(t, 16, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls, active, peak atomic.Int64
	var eventMu sync.Mutex
	var events []string
	var snapshots []Progress
	enter := func() {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
	}
	var progress Progress
	var resultErr error
	go func() {
		defer close(finished)
		progress, resultErr = s.RunAvatarsFor(ctx, names, func(p Progress) {
			enter()
			defer active.Add(-1)
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			eventMu.Lock()
			snapshots = append(snapshots, p)
			events = append(events, "progress:"+p.Current)
			eventMu.Unlock()
		}, func(r AvatarResult) {
			enter()
			defer active.Add(-1)
			eventMu.Lock()
			events = append(events, "item:"+r.Actor)
			eventMu.Unlock()
		})
	}()
	// Ensure the worker is always joined before fixture cleanup, including failures.
	defer func() {
		cancel()
		unblock()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Fatal("avatar workers did not join")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no progress callback")
	}
	// Other workers must reach their completed DB writes while the first callback
	// remains blocked. This is a positive concurrency barrier, not just a sleep.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		indexed := 0
		for _, name := range names {
			a, e := db.ActorAvatar(name)
			if e == nil && a.AvatarTag != "" {
				indexed++
			}
		}
		if indexed >= 8 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("other workers did not finish their actor writes")
		}
	}
	// A bounded negative observation supplements the DB barrier. It is not a
	// general scheduler/liveness proof or a network deadline assertion.
	select {
	case <-time.After(30 * time.Millisecond):
	case <-finished:
		t.Fatal("run completed while callback blocked")
	}
	if calls.Load() != 1 || peak.Load() != 1 {
		t.Errorf("callbacks overtook blocked first delivery: calls=%d peak=%d", calls.Load(), peak.Load())
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not finish")
	}
	if !errors.Is(resultErr, ErrAvatarWriteIncomplete) || progress.Done != 16 || progress.Total != 16 || progress.Success != 8 || progress.Failed != 8 || progress.Skipped != 0 || progress.Consecutive != 0 {
		t.Fatalf("progress=%+v err=%v", progress, resultErr)
	}
	if peak.Load() != 1 || len(snapshots) != 16 || len(events) != 32 {
		t.Fatalf("peak=%d snapshots=%d events=%d", peak.Load(), len(snapshots), len(events))
	}
	seen := map[string]bool{}
	for i, p := range snapshots {
		if p.Done != i+1 || p.Total != 16 || p.Success+p.Failed+p.Skipped != p.Done || seen[p.Current] {
			t.Fatalf("snapshot %d: %+v", i, p)
		}
		seen[p.Current] = true
		if events[2*i] != "progress:"+p.Current || events[2*i+1] != "item:"+p.Current {
			t.Fatalf("interleaved callbacks: %v", events)
		}
	}
}

func TestAvatarConcurrentPartialAggregationWithoutCallbacks(t *testing.T) {
	s, _, names := concurrentAvatarFixture(t, 32, 32)
	p, err := s.RunAvatarsFor(context.Background(), names, nil, nil)
	if !errors.Is(err, ErrAvatarWriteIncomplete) || p.Done != 32 || p.Total != 32 || p.Failed != 32 || p.Success != 0 || p.Consecutive != 0 {
		t.Fatalf("progress=%+v err=%v", p, err)
	}
}
