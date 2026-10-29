package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"
)

var ErrArtworkChanged = errors.New("artwork source or index changed; retry")

// CompareAndSwapArtwork serializes final filesystem CAS with index writers.
// install checks cancellation and disk state before atomically installing an
// image. A subsequent transaction failure leaves a recoverable image on disk.
// Only poster_path changes; metadata, status, userdata and timestamps stay intact.
func (s *Store) CompareAndSwapArtwork(ctx context.Context, expected Movie, libraryRoot, posterPath string, install func() error) error {
	var version int64
	// A long-running scan may hold the writer mutex. Do not leave a cancelled
	// artwork job waiting indefinitely for it, or spawn a goroutine that later
	// acquires an abandoned lock.
	for !s.writeMu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer s.writeMu.Unlock()
	err := s.writeTxOnce(func(tx *sql.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query("SELECT "+movieCols+" FROM movies WHERE id=?", expected.ID)
		if err != nil {
			return err
		}
		if !rows.Next() {
			rows.Close()
			return ErrArtworkChanged
		}
		current, err := movieScan(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, expected) {
			return ErrArtworkChanged
		}
		var root string
		if err := tx.QueryRow("SELECT path FROM libraries WHERE id=?", expected.LibraryID).Scan(&root); err != nil {
			return err
		}
		if root != libraryRoot {
			return ErrArtworkChanged
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if install != nil {
			if err := install(); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("UPDATE movies SET poster_path=? WHERE id=?", posterPath, expected.ID); err != nil {
			return err
		}
		if err := InvalidateScanGroupsForPathsTx(tx, expected.LibraryID, []string{expected.SourcePath}); err != nil {
			return err
		}
		return bumpLibraryVersionTx(tx, expected.LibraryID)
	})
	if err == nil {
		s.publishVersion(version)
	}
	return err
}
