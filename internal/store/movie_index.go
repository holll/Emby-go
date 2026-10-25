package store

import (
	"database/sql"
	"errors"
	"strings"
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
		if err := invalidateMovieScanStateTx(tx, m); err != nil {
			return err
		}
		return refreshFeaturesTx(tx, id)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// IndexMovieUnreadableNFO indexes a source whose sidecar exists but cannot be
// parsed. An existing row keeps its metadata, actors, features and visibility;
// only the file-derived columns follow the source, and the parse error is
// recorded. A source that was never indexed is inserted as pending so the
// operator still sees it.
func (s *Store) IndexMovieUnreadableNFO(m Movie, size int64, mtime time.Time, note string) error {
	return s.writeVersioned(m.LibraryID, func(tx *sql.Tx) error {
		var previousID, previousLibrary sql.NullInt64
		err := tx.QueryRow("SELECT id, library_id FROM movies WHERE source_path=?", m.SourcePath).Scan(&previousID, &previousLibrary)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previousID.Valid && previousLibrary.Valid && previousLibrary.Int64 == m.LibraryID {
			entry := ScanGroupMovie{Movie: m, Size: size, Mtime: mtime, KeepIndexedMetadata: true, MetadataError: note}
			_, err := preserveIndexedMetadataTx(tx, previousID.Int64, entry)
			return err
		}
		if previousID.Valid && previousLibrary.Valid {
			// The source moved in from another library: retire that library's
			// cached view before the new owner takes the row.
			if err := bumpLibraryVersionTx(tx, previousLibrary.Int64); err != nil {
				return err
			}
		}
		return s.reindexUnreadableNFO(tx, m, size, mtime, note)
	})
}

func (s *Store) reindexUnreadableNFO(tx *sql.Tx, m Movie, size int64, mtime time.Time, note string) error {
	id, err := upsertMovieTx(tx, m, size, mtime)
	if err != nil {
		return err
	}
	if err := replaceActorsTx(tx, id, nil); err != nil {
		return err
	}
	if err := invalidateMovieScanStateTx(tx, m); err != nil {
		return err
	}
	if err := setScanNoteTx(tx, id, note); err != nil {
		return err
	}
	return refreshFeaturesTx(tx, id)
}

// ClearScanNFONote drops an "unreadable NFO" note once the sidecar parses again.
// It reads first so the common case (no note) costs no write lock; scrape
// outcomes and operator confirmations share the column but other prefixes.
func (s *Store) ClearScanNFONote(movieID int64) error {
	var note string
	err := s.db.QueryRow(`SELECT COALESCE(last_scrape_error,'') FROM movies WHERE id=?`, movieID).Scan(&note)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if !strings.HasPrefix(note, ScanNFONotePrefix) {
		return nil
	}
	_, err = s.db.Exec(`UPDATE movies SET last_scrape_error='' WHERE id=? AND last_scrape_error LIKE ?`, movieID, ScanNFONotePrefix+"%")
	return err
}

// invalidateMovieScanStateTx drops the incremental baseline for the indexed
// source and all of its parts. Any metadata/actor/feature write may change what
// a later incremental pass would consider unchanged.
func invalidateMovieScanStateTx(tx *sql.Tx, m Movie) error {
	paths := append([]string{m.SourcePath}, m.AdditionalParts...)
	// Source paths are globally unique in movies, but a move between libraries
	// must retire the previous library's baseline as well as the new one.
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM scan_groups WHERE (library_id, group_key) IN (
			SELECT library_id, group_key FROM scan_group_sources WHERE source_path = ?)`, path); err != nil {
			return err
		}
	}
	return nil
}

func invalidateMovieScanStateByIDTx(tx *sql.Tx, movieID int64) error {
	_, err := tx.Exec(`DELETE FROM scan_groups WHERE (library_id, group_key) IN (
		SELECT sg.library_id, sg.group_key FROM scan_group_sources sg JOIN movies m ON m.source_path = sg.source_path WHERE m.id = ?)`, movieID)
	return err
}

func invalidateActorScanStateTx(tx *sql.Tx, name string) error {
	_, err := tx.Exec(`DELETE FROM scan_groups WHERE (library_id, group_key) IN (
		SELECT sg.library_id, sg.group_key FROM scan_group_sources sg
		JOIN movies m ON m.source_path = sg.source_path JOIN movie_actors ma ON ma.movie_id = m.id
		WHERE ma.actor_name = ?)`, name)
	return err
}
