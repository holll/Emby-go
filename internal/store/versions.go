package store

import (
	"database/sql"
	"strconv"
)

// BumpVersion atomically advances global and library versions. Publish the exact
// committed global value monotonically, never a process-local increment.
// Business changes committed separately are NOT made atomic by this method.
func (s *Store) BumpVersion(libraryID int64) error {
	return s.writeVersioned(libraryID, nil)
}

// writeVersioned acquires SQLite's write lock by advancing tentative versions
// BEFORE invoking write. A callback can safely read old state without upgrading
// a stale deferred read snapshot. All changes still roll back together.
func (s *Store) writeVersioned(libraryID int64, write func(*sql.Tx) error) error {
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		version, err = bumpVersionTx(tx, libraryID)
		if err != nil {
			return err
		}
		if write != nil {
			return write(tx)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publishVersion(version)
	return nil
}

// bumpVersionTx changes both version domains in the caller's transaction. The
// returned global version is tentative: publish it ONLY after Commit succeeds.
// Keeping this helper transaction-scoped allows business writes to adopt the
// same atomic boundary incrementally, without publishing uncommitted versions.
func bumpVersionTx(tx *sql.Tx, libraryID int64) (int64, error) {
	version, err := bumpGlobalVersionTx(tx)
	if err != nil {
		return 0, err
	}
	if err := bumpLibraryVersionTx(tx, libraryID); err != nil {
		return 0, err
	}
	return version, nil
}

// A global-only reservation is useful when a business write discovers the
// affected library inside the transaction. Library 0 remains a real version key,
// not a sentinel for skipping the library domain.
func bumpGlobalVersionTx(tx *sql.Tx) (int64, error) {
	var version int64
	err := tx.QueryRow(`INSERT INTO kv(key,value) VALUES('g:version','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1
		RETURNING value`).Scan(&version)
	return version, err
}

func bumpLibraryVersionTx(tx *sql.Tx, libraryID int64) error {
	key := "lib:" + strconv.FormatInt(libraryID, 10) + ":version"
	_, err := tx.Exec(`INSERT INTO kv(key,value) VALUES(?,'1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`, key)
	return err
}

// publishVersion tolerates post-commit goroutines resuming out of order. A late
// publisher must not replace a newer version with an older committed value.
// This is a local mirror, not cross-process cache coherence or a restore epoch.
func (s *Store) publishVersion(version int64) {
	for current := s.gversion.Load(); version > current; current = s.gversion.Load() {
		if s.gversion.CompareAndSwap(current, version) {
			return
		}
	}
}
