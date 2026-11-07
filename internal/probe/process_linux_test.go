//go:build linux

package probe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProbeProcessTreeCleanup(t *testing.T) {
	for _, mode := range []string{"tree", "tree-exit", "tree-overflow"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "child-pid")
			path := probeLauncher(t, mode, marker)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, err := Run(ctx, path, "fixture", 5*time.Second); done <- err }()
			awaitProbeMarker(t, marker)
			raw, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			// Only test cleanup: prevent a failed assertion leaving a live fixture.
			defer syscall.Kill(pid, syscall.SIGKILL)
			if mode == "tree" {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if mode == "tree-exit" {
					want = exec.ErrWaitDelay
				} else if mode == "tree-overflow" {
					want = ErrStdoutTooLarge
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("inherited pipe was not bounded")
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("process tree cleanup exceeded expected bound")
			}
			// SIGKILL makes descendants non-running. Reaping orphan zombies is the
			// host init's responsibility; /proc may retain them after they are dead.
			deadline := time.Now().Add(time.Second)
			for {
				stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if errors.Is(err, os.ErrNotExist) {
					break
				}
				if err == nil {
					state := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
					if len(state) > 0 && (state[0] == "Z" || state[0] == "X") {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant %d remains live: %s (%v)", pid, stat, err)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
