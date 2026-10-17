package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCloseWaitsForInitializationBackfill(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "close.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.waitFeaturesReady()
	// Replace the already-completed signal with a controlled initialization
	// barrier; no real worker still references it.
	gate := make(chan struct{})
	s.featuresReady = gate
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	t.Cleanup(func() { s.Close() })
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Close returned before backfill finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	// The worker's database must still be available until its completion signal.
	if _, err := s.db.Exec("SELECT 1"); err != nil {
		t.Fatalf("DB closed ahead of worker: %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not finish after backfill")
	}
	if _, err := s.db.Exec("SELECT 1"); err == nil {
		t.Fatal("DB still open after Close")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}
