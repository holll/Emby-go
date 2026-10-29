package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"time"
)

// writeTx owns a pinned SQLite connection and a transaction, but does not bump
// cache versions. Callbacks must not commit/rollback or publish tentative state.
func (s *Store) writeTx(write func(*sql.Tx) error) error {
	return s.writeTxContext(context.Background(), write)
}

// writeTxContext is writeTx for callers that own a request deadline. It gives up
// while waiting for the writer lock or a pooled connection instead of holding a
// caller's mutex long after its request was cancelled.
func (s *Store) writeTxContext(ctx context.Context, write func(*sql.Tx) error) error {
	if err := s.lockWriter(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = s.writeTxOnceContext(ctx, write)
		if !isBusyErr(err) {
			return err
		}
		wait := time.Duration(10<<attempt) * time.Millisecond
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func (s *Store) lockWriter(ctx context.Context) error {
	for !s.writeMu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// writeTxOnce pins one connection and executes one write transaction. The
// callback must be safe to re-run: the wrapper retries SQLITE_BUSY, which can
// happen when another process commits between a deferred BEGIN and its first
// write (busy_timeout cannot retry that snapshot upgrade).
func (s *Store) writeTxOnce(write func(*sql.Tx) error) error {
	return s.writeTxOnceContext(context.Background(), write)
}

func (s *Store) writeTxOnceContext(ctx context.Context, write func(*sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			discardWriteConn(conn)
		}
	}()
	if err := write(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		// database/sql marks Tx done even when SQLite rejects COMMIT. A
		// deferred constraint can leave the physical transaction open; only
		// this same pinned connection can safely clean it up. Failed cleanup
		// must discard the connection, never return poisoned state to pool.
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			discardWriteConn(conn)
		}
		return err
	}
	return nil
}

func isBusyErr(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "database is locked") || strings.Contains(message, "SQLITE_BUSY")
}

func discardWriteConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}
