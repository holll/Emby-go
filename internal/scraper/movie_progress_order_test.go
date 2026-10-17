package scraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
)

func movieProgressCandidates(n int) []store.Movie {
	movies := make([]store.Movie, n)
	for i := range movies {
		movies[i] = store.Movie{ID: int64(i + 1), Number: fmt.Sprintf("ABC-%03d", 100+i), Title: fmt.Sprintf("Movie %03d", i)}
	}
	return movies
}

func TestMovieConcurrentProgressDeliveryIsOrdered(t *testing.T) {
	var arrived atomic.Int64
	gate := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == 8 {
			openGate()
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("q"), "ABC-"))
		if n < 108 {
			http.Error(w, "fixture denied", http.StatusUnauthorized)
		} else {
			http.Error(w, "no result", http.StatusNotFound)
		}
	}))
	defer backend.Close()
	s := New(Config{BaseURL: backend.URL, Token: "fixture", Concurrency: 8}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var calls, active, peak atomic.Int64
	var mu sync.Mutex
	var snapshots []Progress
	var events []string
	enter := func() {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
	}
	var progress Progress
	var resultErr error
	go func() {
		defer close(finished)
		progress, resultErr = s.RunMovies(ctx, movieProgressCandidates(16), RunOptions{
			OnProgress: func(p Progress) {
				enter()
				defer active.Add(-1)
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				mu.Lock()
				snapshots = append(snapshots, p)
				events = append(events, "progress:"+p.Current)
				mu.Unlock()
			},
			OnItem: func(r ItemResult) {
				enter()
				defer active.Add(-1)
				mu.Lock()
				events = append(events, "item:"+r.Title)
				mu.Unlock()
			},
		})
	}()
	defer func() {
		cancel()
		openGate()
		unblock()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("movie workers did not join")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no progress delivery")
	}
	if arrived.Load() < 8 {
		t.Fatal("transport concurrency barrier not reached")
	}
	// A bounded negative observation, not a general scheduler/cancellation proof.
	select {
	case <-time.After(30 * time.Millisecond):
	case <-finished:
		t.Fatal("run finished during blocked callback")
	}
	if calls.Load() != 1 || peak.Load() != 1 {
		t.Errorf("movie callbacks overtook blocked first delivery: calls=%d peak=%d", calls.Load(), peak.Load())
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("movie run did not finish")
	}
	if resultErr != nil || progress.Done != 16 || progress.Total != 16 || progress.Failed != 8 || progress.Skipped != 8 || progress.Success != 0 {
		t.Fatalf("progress=%+v err=%v", progress, resultErr)
	}
	if peak.Load() != 1 || len(snapshots) != 16 || len(events) != 32 {
		t.Fatalf("peak=%d snapshots=%d events=%d", peak.Load(), len(snapshots), len(events))
	}
	seen := map[string]bool{}
	for i, p := range snapshots {
		if p.Done != i+1 || p.Success+p.Skipped+p.Failed != p.Done || seen[p.Current] {
			t.Fatalf("snapshot %d=%+v", i, p)
		}
		seen[p.Current] = true
		if events[2*i] != "progress:"+p.Current || events[2*i+1] != "item:"+p.Current {
			t.Fatalf("interleaved movie callbacks: %v", events)
		}
	}
	if progress != snapshots[len(snapshots)-1] {
		t.Fatal("returned progress differs from final snapshot")
	}
}

func TestMovieBreakerThresholdResetAndCancellationIdentity(t *testing.T) {
	for _, mode := range []string{"below", "threshold", "cancel", "reset_skipped", "reset_nonfatal"} {
		t.Run(mode, func(t *testing.T) {
			total := MaxConsecutiveFailures + 5
			reset := strings.HasPrefix(mode, "reset_")
			if mode == "below" {
				total = MaxConsecutiveFailures - 1
			}
			if reset {
				total = 2*MaxConsecutiveFailures - 1
			}
			var requests atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(requests.Add(1))
				if reset && n == MaxConsecutiveFailures {
					if mode == "reset_skipped" {
						http.Error(w, "no result", http.StatusNotFound)
					} else {
						http.Error(w, "incomplete metadata", http.StatusInternalServerError)
					}
					return
				}
				http.Error(w, "fixture denied", http.StatusUnauthorized)
			}))
			defer backend.Close()
			s := New(Config{BaseURL: backend.URL, Token: "fixture", Concurrency: 1}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var last Progress
			var items []ItemResult
			p, err := s.RunMovies(ctx, movieProgressCandidates(total), RunOptions{
				OnProgress: func(p Progress) { last = p },
				OnItem: func(item ItemResult) {
					items = append(items, item)
					if mode == "cancel" && last.Consecutive == MaxConsecutiveFailures {
						cancel()
					}
				},
			})
			breaker := mode == "threshold" || mode == "cancel"
			done := total
			if breaker {
				done = MaxConsecutiveFailures
			}
			if p != last || p.Done != done || p.Total != total || requests.Load() != int64(done) || len(items) != done {
				t.Fatalf("progress=%+v last=%+v requests=%d items=%d", p, last, requests.Load(), len(items))
			}
			if errors.Is(err, context.Canceled) != (mode == "cancel") {
				t.Fatalf("movie cancellation identity lost: %v", err)
			}
			if breaker {
				message := fmt.Sprintf("连续 %d 条刮削失败，疑似 MetaTube 不可达或 token 失效，任务已提前中止（已处理 %d/%d）", MaxConsecutiveFailures, done, total)
				if err == nil || !strings.Contains(err.Error(), message) || p.Consecutive != MaxConsecutiveFailures {
					t.Fatalf("breaker=%+v err=%v", p, err)
				}
				if mode == "threshold" && err.Error() != message {
					t.Fatalf("ordinary breaker text changed: %v", err)
				}
			} else if err != nil || p.Consecutive != MaxConsecutiveFailures-1 {
				t.Fatalf("unexpected breaker=%+v err=%v", p, err)
			}
			skipped := 0
			if mode == "reset_skipped" {
				skipped = 1
			}
			if p.Skipped != skipped || p.Failed != done-skipped || p.Success != 0 {
				t.Fatalf("counts=%+v", p)
			}
		})
	}
}
