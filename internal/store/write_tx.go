package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

// writeTx owns a pinned SQLite connection and a transaction, but does not bump
// cache versions. Callbacks must not commit/rollback or publish tentative state.
func (s *Store) writeTx(write func(*sql.Tx) error) error {
	ctx := context.Background()
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
		if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
			discardWriteConn(conn)
		}
		return err
	}
	return nil
}

func discardWriteConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}
