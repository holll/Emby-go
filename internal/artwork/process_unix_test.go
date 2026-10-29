//go:build unix

package artwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessOverflowTerminatesPromptly(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			script := "while :; do printf 'private-token-012345678901234567890123456789'; done"
			if stream == "stderr" {
				script = "exec 1>&2; " + script
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			start := time.Now()
			_, err := command(ctx, "sh", []string{"-c", script}, 64)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("overflow did not stop subprocess: %v", err)
			}
			if time.Since(start) > time.Second {
				t.Fatalf("overflow took %v", time.Since(start))
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatal("subprocess diagnostics leaked")
			}
		})
	}
}

func TestCancelTerminatesWrapperChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := command(ctx, "sh", []string{"-c", `(sleep 0.3; printf survived > "$1") & wait`, "artwork", marker}, 64)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-time.After(400 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived cancellation: %v", err)
	}
}
