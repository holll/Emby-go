package store

import (
	"database/sql"
	"errors"
	"time"
)

// IndexMovie commits metadata, actors, derived features and cache versions as
// one unit. It does not write NFO files or make an entire scan atomic.
func (s *Store) IndexMovie(m Movie, size int64, mtime time.Time, actors []ActorRef) (int64, error) {
	var id int64
	err := s.writeVersioned(m.LibraryID, func(tx *sql.Tx) error {
		// writeVersioned already holds the write reservation. Reading the
		// previous library cannot race another writer's move of this source.
		var previous sql.NullInt64
		err := tx.QueryRow("SELECT library_id FROM movies WHERE source_path=?", m.SourcePath).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previous.Valid && previous.Int64 != m.LibraryID {
			if err := bumpLibraryVersionTx(tx, previous.Int64); err != nil {
				return err
			}
		}
		id, err = upsertMovieTx(tx, m, size, mtime)
		if err != nil {
			return err
		}
		if err := replaceActorsTx(tx, id, actors); err != nil {
			return err
		}
		return refreshFeaturesTx(tx, id)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}
