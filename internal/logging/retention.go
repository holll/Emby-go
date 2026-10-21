package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxRetentionDays is the largest supported log retention window.
const MaxRetentionDays = 3650

const retentionWarning = "[warn] log retention cleanup failed; normal logging continues\n"

func validateRetentionDays(days int) error {
	if days < 0 || days > MaxRetentionDays {
		return fmt.Errorf("log retention days must be between 0 and %d", MaxRetentionDays)
	}
	return nil
}

// SetRetentionDays applies to both currently configured loggers, immediately
// cleaning up old files. Zero disables retention (the default). Before Setup it
// is a no-op. Invalid values never change policy or delete files. Cleanup errors
// are returned, but the new policy remains installed even after partial cleanup.
func SetRetentionDays(days int) error {
	if err := validateRetentionDays(days); err != nil {
		return err
	}
	currentMu.Lock()
	defer currentMu.Unlock()
	if current == nil {
		return nil
	}
	return errors.Join(current.app.SetRetentionDays(days), current.request.SetRetentionDays(days))
}

// SetRetentionDays installs a retention window of today plus the previous days-1
// local calendar dates and immediately purges older filename dates. Zero disables
// cleanup. An error does not roll back the installed policy or completed deletes.
func (w *DailyWriter) SetRetentionDays(days int) error {
	if err := validateRetentionDays(days); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retentionDays = days
	w.purgedDay = ""
	if days == 0 {
		return nil
	}
	now := w.now()
	err := w.purgeLocked(now)
	// Applying just after midnight may skip the still-open previous file. Keep
	// the first-write pass pending so rotation can close and then purge it.
	if w.file != nil && w.day != now.Format(dailyLayout) {
		w.purgedDay = ""
	}
	return err
}

// purgeLocked never logs through slog (which could call this writer again).
// Failed attempts also consume the day's automatic cleanup, avoiding repeated IO
// and warnings on each Write. An explicit policy application always retries.
func (w *DailyWriter) purgeLocked(now time.Time) error {
	day := now.Format(dailyLayout)
	w.purgedDay = day
	err := w.cleanupLocked(day)
	if err != nil && w.warnedDay != day {
		w.warnedDay = day
		_, _ = w.console.Write([]byte(retentionWarning))
	}
	return err
}

func (w *DailyWriter) cleanupLocked(day string) error {
	// Only the two known log namespaces are eligible, even if a caller constructs
	// a writer with a different (or path-containing) prefix.
	if w.prefix != "app" && w.prefix != "request" {
		return nil
	}
	// Treat dates as calendar labels rather than elapsed durations. UTC here is
	// only date arithmetic: day itself was sampled in the writer's local zone.
	today, err := time.Parse(dailyLayout, day)
	if err != nil {
		return err
	}
	cutoff := today.AddDate(0, 0, 1-w.retentionDays).Format(dailyLayout)
	dir, err := filepath.Abs(w.dir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("log retention root is not a real directory")
	}
	if w.dirIdentity == nil {
		w.dirIdentity = info
	}
	if !os.SameFile(w.dirIdentity, info) {
		return errors.New("log retention root changed")
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // A writer need not have created its directory yet.
	}
	if err != nil {
		return err
	}
	defer root.Close()
	// Compare the opened handle too: a rename/symlink swap between Lstat and
	// OpenRoot must not redirect deletions into a different directory.
	openedInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(w.dirIdentity, openedInfo) {
		return errors.New("log retention root changed")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, readErr := directory.ReadDir(-1)
	errs := []error{readErr}
	for _, entry := range entries {
		name := entry.Name()
		// Resolve only a constrained single basename inside the opened root.
		if filepath.Base(name) != name || !strings.HasPrefix(name, w.prefix+"-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		date := strings.TrimSuffix(strings.TrimPrefix(name, w.prefix+"-"), ".log")
		parsed, err := time.Parse(dailyLayout, date)
		if err != nil || parsed.Format(dailyLayout) != date || date >= cutoff {
			continue
		}
		if w.file != nil && name == w.prefix+"-"+w.day+".log" {
			continue
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
