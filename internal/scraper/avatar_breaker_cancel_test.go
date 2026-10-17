package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAvatarBreakerThresholdResetAndCancellationIdentity(t *testing.T) {
	for _, mode := range []string{"below", "threshold", "cancel", "partial_cancel", "reset_success", "reset_skipped", "reset_nonfatal"} {
		t.Run(mode, func(t *testing.T) {
			total := MaxConsecutiveFailures + 5
			if mode == "below" {
				total = MaxConsecutiveFailures - 1
			}
			reset := strings.HasPrefix(mode, "reset_")
			if reset {
				total = 2*MaxConsecutiveFailures - 1
			}
			partial := 0
			if mode == "partial_cancel" {
				partial = 1
			}
			original, db, names := concurrentAvatarFixture(t, total, partial)
			picture := avatarPNG(t, 1)
			var requests atomic.Int64
			var backend *httptest.Server
			backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/picture.png" {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(picture)
					return
				}
				requests.Add(1)
				name := r.URL.Query().Get("q")
				if mode == "partial_cancel" && name == names[0] || mode == "reset_success" && name == names[MaxConsecutiveFailures-1] {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "one", "name": name, "provider": "fixture", "images": []string{backend.URL + "/picture.png"}}}})
					return
				}
				if mode == "reset_skipped" && name == names[MaxConsecutiveFailures-1] {
					http.Error(w, "no result", http.StatusNotFound)
					return
				}
				if mode == "reset_nonfatal" && name == names[MaxConsecutiveFailures-1] {
					http.Error(w, "incomplete metadata", http.StatusInternalServerError)
					return
				}
				http.Error(w, "fixture denied", http.StatusUnauthorized)
			}))
			defer backend.Close()
			cfg := original.cfg
			cfg.Concurrency = 1
			cfg.BaseURL = backend.URL
			s := New(cfg, db)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var last Progress
			var items []AvatarResult
			progress, err := s.RunAvatarsFor(ctx, names, func(p Progress) { last = p }, func(item AvatarResult) {
				items = append(items, item)
				if (mode == "cancel" || mode == "partial_cancel") && last.Consecutive == MaxConsecutiveFailures {
					cancel()
				}
			})
			wantCanceled := mode == "cancel" || mode == "partial_cancel"
			breaker := !reset && mode != "below"
			wantDone := total
			if breaker {
				wantDone = MaxConsecutiveFailures + partial
			}
			if progress != last || progress.Done != wantDone || progress.Total != total || len(items) != wantDone || requests.Load() != int64(wantDone) {
				t.Fatalf("progress=%+v last=%+v items=%d requests=%d", progress, last, len(items), requests.Load())
			}
			if errors.Is(err, context.Canceled) != wantCanceled {
				t.Fatalf("cancellation identity lost: %v", err)
			}
			if errors.Is(err, ErrAvatarWriteIncomplete) != (partial > 0) {
				t.Fatalf("partial identity lost: %v", err)
			}
			if breaker {
				message := fmt.Sprintf("连续 %d 个演员头像失败，疑似 MetaTube 不可达或 token 失效，任务已提前中止", MaxConsecutiveFailures)
				if err == nil || !strings.Contains(err.Error(), message) || progress.Consecutive != MaxConsecutiveFailures {
					t.Fatalf("breaker=%+v err=%v", progress, err)
				}
				if mode == "threshold" && err.Error() != message {
					t.Fatalf("ordinary breaker text changed: %v", err)
				}
			} else if err != nil || progress.Consecutive != MaxConsecutiveFailures-1 {
				t.Fatalf("unexpected breaker=%+v err=%v", progress, err)
			}
			success, skipped := 0, 0
			if mode == "reset_success" {
				success = 1
			}
			if mode == "reset_skipped" {
				skipped = 1
			}
			if progress.Success != success || progress.Skipped != skipped || progress.Failed != wantDone-success-skipped {
				t.Fatalf("counts=%+v", progress)
			}
			fatal := 0
			for _, item := range items {
				if item.Fatal {
					fatal++
				}
			}
			wantFatal := wantDone - partial - success - skipped
			if mode == "reset_nonfatal" {
				wantFatal--
			}
			if fatal != wantFatal {
				t.Fatalf("fatal count=%d want=%d", fatal, wantFatal)
			}
		})
	}
}
