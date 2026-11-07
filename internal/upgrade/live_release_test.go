package upgrade

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveReleaseUpgrade downloads the current public release and replaces a
// throwaway binary. It talks to GitHub and is opt-in:
//
//	EMBY_UPGRADE_LIVE=1 go test ./internal/upgrade -run '^TestLiveReleaseUpgrade$'
//
// It never touches the installed service or the repository binary.
func TestLiveReleaseUpgrade(t *testing.T) {
	if os.Getenv("EMBY_UPGRADE_LIVE") != "1" {
		t.Skip("set EMBY_UPGRADE_LIVE=1 to run the live release upgrade")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	installed := filepath.Join(dir, "emby-go")
	if err := os.WriteFile(installed, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := New(Options{CurrentVersion: "v2.2.6"})
	m.executable = installed
	result, err := m.Check(ctx, true)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !result.Available || result.Latest == "" {
		t.Fatalf("expected an available release, got %+v", result)
	}
	t.Logf("latest=%s published=%s", result.Latest, result.PublishedAt.Format(time.RFC3339))

	if err := m.Begin(result.Latest); err != nil {
		t.Fatalf("begin: %v", err)
	}
	m.Run(ctx, result.Latest)

	select {
	case <-m.Restart():
	case <-ctx.Done():
		t.Fatalf("restart signal not delivered: %v", ctx.Err())
	}
	state := m.State()
	if state.Phase != PhaseRestarting {
		t.Fatalf("phase = %q, message = %q", state.Phase, state.Message)
	}

	out, err := exec.Command(installed, "-version").Output()
	if err != nil {
		t.Fatalf("installed binary -version: %v", err)
	}
	version := strings.TrimSpace(string(out))
	t.Logf("replaced binary: %s", version)
	if !strings.Contains(version, result.Latest) {
		t.Fatalf("installed binary version %q does not contain %q", version, result.Latest)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Fatalf("temporary download files not cleaned up: %v", entries)
	}
}
