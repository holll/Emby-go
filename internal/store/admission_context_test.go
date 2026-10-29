package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A cancelled administrator request must not keep waiting for the SQLite writer.
// Handlers hold a cross-request edit lock while admitting, so an admission that
// outlives its request context stalls every other administrator.
func TestAdmitAdminOperationContextCancellation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "admission.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	locked := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- s.writeTx(func(*sql.Tx) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := s.AdmitAdminOperationContext(cancelled, User{ID: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled admission waited %v", elapsed)
	}

	expiring, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	start = time.Now()
	if err := s.AdmitAdminOperationContext(expiring, User{ID: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired admission %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("expired admission waited %v", elapsed)
	}

	close(release)
	if err := <-holder; err != nil {
		t.Fatalf("writer transaction %v", err)
	}
	// Once the writer is free the same call succeeds, proving the bounded form
	// did not permanently poison the lock.
	if err := s.AdmitAdminOperationContext(context.Background(), User{ID: 1}); !errors.Is(err, ErrAdminGenerationStale) {
		t.Fatalf("admission after release %v", err)
	}
}
