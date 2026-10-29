package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"time"
)

// writerGate is the in-process single-writer lock: a buffered channel of
// capacity 1 used as a semaphore (P2-13).
//
// Why not sync.Mutex + TryLock polling (the previous shape): every waiter woke
// up 200 times/second (a 5ms timer per attempt) and TryLock is not FIFO, so a
// waiter could lose the race repeatedly while later arrivals passed it. Senders
// parked on a buffered channel are queued by the runtime in arrival order, so
// the hand-off is FIFO (no starvation) and a parked waiter burns no timers.
//
// Why a channel and not sync.Cond: the wait must also abandon on ctx.Done()
// (P1-5: a cancelled scan commit has to stop waiting for the writer lock).
// select over a channel send plus ctx.Done() gives FIFO ordering and
// cancellation; a Cond cannot be released by a context.
//
// The zero value is usable: the channel is created lazily (see init), so a
// &Store{} that never went through Open still cannot park forever (a send on a
// nil channel would wait indefinitely). Production always uses Open, but tests
// and future value construction must not deadlock.
type writerGate struct {
	once sync.Once
	ch   chan struct{}
}

func (g *writerGate) init() {
	g.once.Do(func() { g.ch = make(chan struct{}, 1) })
}

// Lock blocks until the gate is free. It cannot be cancelled; callers that own a
// deadline use LockContext.
func (g *writerGate) Lock() {
	g.init()
	g.ch <- struct{}{}
}

// LockContext blocks until the gate is free or ctx is done. The returned error
// means the caller never held the gate. A token acquired in the same instant the
// context was cancelled is handed back before returning, so a cancelled caller
// can never keep (and later "release") the writer lock.
func (g *writerGate) LockContext(ctx context.Context) error {
	g.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case g.ch <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.ch
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryLock reports whether the gate was free and takes it without blocking. Tests
// use it as an "is a writer holding the lock right now" probe, so it must stay
// non-blocking and side-effect free when it returns false.
func (g *writerGate) TryLock() bool {
	g.init()
	select {
	case g.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Unlock releases the gate by draining the token; if waiters are parked the
// runtime hands the token straight to the first of them (FIFO). An empty buffer
// means Unlock ran without a matching Lock; surface that as a panic like
// sync.Mutex does instead of silently corrupting the invariant.
func (g *writerGate) Unlock() {
	g.init()
	select {
	case <-g.ch:
	default:
		panic("store: writerGate unlocked while not held")
	}
}

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
	for attempt := range 3 {
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
	// P2-13: park in the gate's FIFO queue instead of polling TryLock every 5ms.
	// writerGate.LockContext keeps the P1-5 contract that a cancelled caller
	// abandons the wait immediately (and never leaves the token behind).
	return s.writeMu.LockContext(ctx)
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
