package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func retentionFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func retentionExists(t *testing.T, dir, name string, want bool) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, name))
	if want && err != nil || !want && !os.IsNotExist(err) {
		t.Errorf("%s exists: error=%v, want exists=%v", name, err, want)
	}
}

func TestRetentionCalendarBoundaries(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		day, cutoff, expired string
		days                 int
	}{
		{"2026-03-09", "2026-03-08", "2026-03-07", 2}, // 23-hour DST day
		{"2026-11-02", "2026-11-01", "2026-10-31", 2}, // 25-hour DST day
		{"2026-01-01", "2025-12-31", "2025-12-30", 2},
		{"2024-03-01", "2024-02-29", "2024-02-28", 2},
		{"2026-05-01", "2026-05-01", "2026-04-30", 1},
	} {
		t.Run(tc.day, func(t *testing.T) {
			dir := t.TempDir()
			w := NewDailyWriter(dir, "app", io.Discard)
			t.Cleanup(func() { _ = w.Close() })
			now, err := time.ParseInLocation("2006-01-02 15:04", tc.day+" 00:30", zone)
			if err != nil {
				t.Fatal(err)
			}
			w.now = func() time.Time { return now }
			retentionFile(t, dir, "app-"+tc.day+".log")
			keep := retentionFile(t, dir, "app-"+tc.cutoff+".log")
			expired := retentionFile(t, dir, "app-"+tc.expired+".log")
			// Filename dates, not modification times, define eligibility.
			if err := os.Chtimes(keep, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
				t.Fatal(err)
			}
			future := now.AddDate(1, 0, 0)
			if err := os.Chtimes(expired, future, future); err != nil {
				t.Fatal(err)
			}
			if err := w.SetRetentionDays(tc.days); err != nil {
				t.Fatal(err)
			}
			retentionExists(t, dir, "app-"+tc.day+".log", true)
			retentionExists(t, dir, "app-"+tc.cutoff+".log", true)
			retentionExists(t, dir, "app-"+tc.expired+".log", false)
		})
	}
}

func TestRetentionOnlyOwnExactRegularFiles(t *testing.T) {
	dir := t.TempDir()
	w := NewDailyWriter(dir, "app", io.Discard)
	w.now = func() time.Time { return time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC) }
	keep := []string{
		"request-2020-01-01.log", "app-2020-1-01.log", "app-2020-02-30.log",
		"app-2020-01-01.log.bak", "app-2020-01-01.extra.log", "xapp-2020-01-01.log",
		"app-2026-05-04.log", "app-2030-01-01.log", "app-2020-01-01.log\n",
	}
	for _, name := range keep {
		retentionFile(t, dir, name)
	}
	retentionFile(t, dir, "app-2020-01-01.log")
	retentionFile(t, dir, "app-2020-01-02.log")
	// Valid-looking directories (including contents) and symlinks survive.
	subdir := filepath.Join(dir, "app-2020-01-03.log")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	retentionFile(t, subdir, "app-2020-01-01.log")
	outside := t.TempDir()
	target := retentionFile(t, outside, "target")
	if err := os.Symlink(target, filepath.Join(dir, "app-2020-01-04.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(dir, "app-2020-01-05.log")); err != nil {
		t.Fatal(err)
	}
	if err := w.SetRetentionDays(1); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(keep, "app-2020-01-03.log", "app-2020-01-04.log", "app-2020-01-05.log") {
		retentionExists(t, dir, name, true)
	}
	retentionExists(t, dir, "app-2020-01-01.log", false)
	retentionExists(t, dir, "app-2020-01-02.log", false)
	retentionExists(t, subdir, "app-2020-01-01.log", true)
	retentionExists(t, outside, "target", true)
	// Other prefixes cannot be used as cleanup namespaces.
	unknown := NewDailyWriter(dir, "xapp", io.Discard)
	if err := unknown.SetRetentionDays(1); err != nil {
		t.Fatal(err)
	}
	retentionExists(t, dir, "xapp-2020-01-01.log", true)
}

func TestRetentionImmediateActiveAndNextDay(t *testing.T) {
	dir := t.TempDir()
	w := NewDailyWriter(dir, "request", io.Discard)
	t.Cleanup(func() { _ = w.Close() })
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	_, _ = w.Write([]byte("first\n"))
	now = now.AddDate(0, 0, 2)
	retentionFile(t, dir, "request-2025-12-01.log")
	if err := w.SetRetentionDays(1); err != nil {
		t.Fatal(err)
	}
	retentionExists(t, dir, "request-2025-12-01.log", false)
	retentionExists(t, dir, "request-2026-01-01.log", true) // active despite stale date
	_, _ = w.Write([]byte("third\n"))
	retentionExists(t, dir, "request-2026-01-03.log", true)
	// The first write rotates the protected old handle and must finish cleanup.
	retentionExists(t, dir, "request-2026-01-01.log", false)
	retentionFile(t, dir, "request-2025-01-01.log")
	_, _ = w.Write([]byte("same day\n"))
	retentionExists(t, dir, "request-2025-01-01.log", true)
	now = now.AddDate(0, 0, 1)
	_, _ = w.Write([]byte("fourth\n"))
	retentionExists(t, dir, "request-2026-01-01.log", false)
	retentionExists(t, dir, "request-2026-01-03.log", false)
	retentionExists(t, dir, "request-2025-01-01.log", false)
	if got := readFile(t, filepath.Join(dir, "request-2026-01-04.log")); got != "fourth\n" {
		t.Fatalf("new day's file = %q", got)
	}
}

func TestRetentionDisabledAndInvalid(t *testing.T) {
	dir := t.TempDir()
	w := NewDailyWriter(dir, "app", io.Discard)
	t.Cleanup(func() { _ = w.Close() })
	retentionFile(t, dir, "app-2000-01-01.log")
	_, _ = w.Write([]byte("default disabled\n"))
	for _, days := range []int{-1, MaxRetentionDays + 1} {
		if err := w.SetRetentionDays(days); err == nil {
			t.Fatalf("invalid days %d accepted", days)
		}
		if w.retentionDays != 0 {
			t.Fatal("invalid update changed policy")
		}
	}
	if err := w.SetRetentionDays(0); err != nil {
		t.Fatal(err)
	}
	retentionExists(t, dir, "app-2000-01-01.log", true)
	if err := w.SetRetentionDays(MaxRetentionDays); err != nil {
		t.Fatal(err)
	}
	if err := w.SetRetentionDays(0); err != nil {
		t.Fatal(err)
	}
	retentionFile(t, dir, "app-2000-01-01.log")
	_, _ = w.Write([]byte("disabled again\n"))
	retentionExists(t, dir, "app-2000-01-01.log", true)
}

func TestRetentionFailureWarningAndRecovery(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "blocked")
	retentionFile(t, base, "blocked") // OpenRoot fails regardless of user privileges.
	var console bytes.Buffer
	w := NewDailyWriter(dir, "app", &console)
	t.Cleanup(func() { _ = w.Close() })
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	for range 2 {
		if err := w.SetRetentionDays(1); err == nil {
			t.Fatal("expected cleanup error")
		}
	}
	if w.retentionDays != 1 {
		t.Fatal("failed cleanup rolled back policy")
	}
	if got := strings.Count(console.String(), retentionWarning); got != 1 {
		t.Fatalf("cleanup warnings = %d", got)
	}
	if n, err := w.Write([]byte("still logging\n")); n != len("still logging\n") || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	now = now.AddDate(0, 0, 1)
	if err := w.SetRetentionDays(1); err == nil {
		t.Fatal("expected cleanup error next day")
	}
	if got := strings.Count(console.String(), retentionWarning); got != 2 {
		t.Fatalf("next day cleanup warnings = %d", got)
	}
	// Repair the directory; existing failed attempts must not discard the policy.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	retentionFile(t, dir, "app-2000-01-01.log")
	now = now.AddDate(0, 0, 1)
	if n, err := w.Write([]byte("recovered\n")); n != len("recovered\n") || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	retentionExists(t, dir, "app-2000-01-01.log", false)
	if got := readFile(t, filepath.Join(dir, "app-2026-05-06.log")); got != "recovered\n" {
		t.Fatalf("recovered log = %q", got)
	}
}

func TestRetentionCleanupFailureDoesNotInterruptOpenWriter(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	w := NewDailyWriter(dir, "app", &console)
	t.Cleanup(func() { _ = w.Close() })
	w.now = func() time.Time { return time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC) }
	_, _ = w.Write([]byte("before\n"))
	// The active descriptor remains writable even when its directory can no
	// longer be opened for cleanup (deterministic also when tests run as root).
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.SetRetentionDays(1); err == nil {
		t.Fatal("expected cleanup failure")
	}
	for range 2 {
		if n, err := w.Write([]byte("after\n")); n != len("after\n") || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got := readFile(t, filepath.Join(moved, "app-2026-05-04.log")); got != "before\nafter\nafter\n" {
		t.Fatalf("file writing interrupted by cleanup error: %q", got)
	}
	if got := strings.Count(console.String(), retentionWarning); got != 1 {
		t.Fatalf("cleanup warnings = %d", got)
	}
	// Restore the temp directory so t.TempDir's cleanup has its original path.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionGlobalAndConcurrentUpdates(t *testing.T) {
	currentMu.Lock()
	previous := current
	current = nil
	currentMu.Unlock()
	t.Cleanup(func() {
		currentMu.Lock()
		current = previous
		currentMu.Unlock()
	})
	if err := SetRetentionDays(1); err != nil {
		t.Fatal(err)
	}
	if err := SetRetentionDays(-1); err == nil {
		t.Fatal("invalid global value accepted")
	}
	dir := t.TempDir()
	app := NewDailyWriter(dir, "app", io.Discard)
	request := NewDailyWriter(dir, "request", io.Discard)
	t.Cleanup(func() { _ = app.Close(); _ = request.Close() })
	for _, prefix := range []string{"app", "request"} {
		retentionFile(t, dir, prefix+"-2000-01-01.log")
	}
	currentMu.Lock()
	current = &Loggers{app: app, request: request}
	currentMu.Unlock()
	if err := SetRetentionDays(2); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"app", "request"} {
		retentionExists(t, dir, prefix+"-2000-01-01.log", false)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for n := range 50 {
				_, _ = AppWriter().Write([]byte("app\n"))
				_, _ = RequestWriter().Write([]byte("request\n"))
				var err error
				if i%2 == 0 {
					err = SetRetentionDays(n % 4)
				} else {
					err = app.SetRetentionDays(n % 4)
				}
				if err != nil {
					t.Error(fmt.Errorf("concurrent update: %w", err))
				}
			}
		})
	}
	wg.Wait()
	// A failure in the app namespace must not prevent request cleanup or roll
	// back either policy: filesystem deletes cannot be treated transactionally.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedApp := NewDailyWriter(blocked, "app", io.Discard)
	currentMu.Lock()
	current = &Loggers{app: failedApp, request: request}
	currentMu.Unlock()
	retentionFile(t, dir, "request-2000-01-01.log")
	if err := SetRetentionDays(1); err == nil {
		t.Fatal("expected global partial cleanup error")
	}
	if failedApp.retentionDays != 1 || request.retentionDays != 1 {
		t.Fatal("partial failure rolled back a policy")
	}
	retentionExists(t, dir, "request-2000-01-01.log", false)
}

func TestRetentionMissingDirectoryAndRelativeDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "not-created")
	w := NewDailyWriter(dir, "app", io.Discard)
	if err := w.SetRetentionDays(1); err != nil {
		t.Fatal(err)
	}
	retentionExists(t, base, "not-created", false)
	// Chdir only into temporary directories: never inspect production logs.
	t.Chdir(base)
	relative := NewDailyWriter("relative", "app", io.Discard)
	if relative.dir != filepath.Join(base, "relative") {
		t.Fatalf("relative directory not pinned: %s", relative.dir)
	}
	if err := os.Mkdir(relative.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	retentionFile(t, relative.dir, "app-2000-01-01.log")
	t.Chdir(t.TempDir())
	if err := relative.SetRetentionDays(1); err != nil {
		t.Fatal(err)
	}
	retentionExists(t, relative.dir, "app-2000-01-01.log", false)
}

func TestRetentionRejectsRootReplacement(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "initial-symlink"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "log")
			moved := filepath.Join(base, "original")
			outside := filepath.Join(base, "outside")
			if !filepath.IsAbs(base) || filepath.Dir(dir) != base || filepath.Dir(moved) != base {
				t.Fatal("unexpected fixture paths")
			}
			if err := os.MkdirAll(outside, 0700); err != nil {
				t.Fatal(err)
			}
			retentionFile(t, outside, "app-2000-01-01.log")
			if kind == "initial-symlink" {
				if err := os.Symlink(outside, dir); err != nil {
					t.Skip(err)
				}
			} else {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			w := NewDailyWriter(dir, "app", io.Discard)
			defer w.Close()
			if kind != "initial-symlink" {
				retentionFile(t, dir, "app-2000-01-01.log")
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(outside, dir); err != nil {
						t.Skip(err)
					}
				} else {
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					retentionFile(t, dir, "app-2000-01-01.log")
				}
			}
			if err := w.SetRetentionDays(1); err == nil {
				t.Fatal("replaced or symlink root accepted")
			}
			retentionExists(t, outside, "app-2000-01-01.log", true)
			if kind != "initial-symlink" {
				retentionExists(t, moved, "app-2000-01-01.log", true)
			}
			if kind == "directory" {
				retentionExists(t, dir, "app-2000-01-01.log", true)
			}
		})
	}
}
