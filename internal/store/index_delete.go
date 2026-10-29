package store

import (
	"database/sql"
	"strings"
)

// DeleteIndexedMovie removes an indexed item and advances its actual library's
// version in the same transaction. No match returns sql.ErrNoRows, without a bump.
// Legacy DeleteMovie intentionally retains its separate, unversioned contract.
func (s *Store) DeleteIndexedMovie(id int64) error {
	_, err := s.DeleteIndexedMovieSnapshot(id)
	return err
}

// DeleteIndexedMovieSnapshot returns ID, library ID, title and source path of
// the committed deleted row (other Movie fields are not populated). Capturing
// the path with DELETE RETURNING avoids notifying about a stale pre-read movie.
// Errors (including COMMIT failure) always return a zero snapshot.
func (s *Store) DeleteIndexedMovieSnapshot(id int64) (Movie, error) {
	var movie Movie
	movie.ID = id
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		// Reserve a writer without touching an unrelated lib:0 domain.
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		var libraryID sql.NullInt64
		if err := tx.QueryRow("DELETE FROM movies WHERE id=? RETURNING library_id,source_path,COALESCE(title,'')", id).Scan(&libraryID, &movie.SourcePath, &movie.Title); err != nil {
			return err
		}
		if libraryID.Valid {
			movie.LibraryID = libraryID.Int64
			if err := InvalidateScanGroupsForPathsTx(tx, movie.LibraryID, []string{movie.SourcePath}); err != nil {
				return err
			}
			return bumpLibraryVersionTx(tx, libraryID.Int64)
		}
		return nil
	})
	if err != nil {
		return Movie{}, err
	}
	s.publishVersion(version)
	return movie, nil
}

// DeleteIndexedLibrary only deletes database records, never files on disk.
// Retain the library's version key so cached library responses become obsolete.
// Legacy DeleteLibrary keeps its separate, unversioned contract.
func (s *Store) DeleteIndexedLibrary(id int64) error {
	return s.writeVersioned(id, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM movies WHERE library_id=?", id); err != nil {
			return err
		}
		result, err := tx.Exec("DELETE FROM libraries WHERE id=?", id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// ReconcileMissingSources assumes paths is a complete inventory of the library.
// The caller must skip it after any walk/read/index failure. The write reservation
// precedes enumeration; deletion of all chunks and both versions commit together.
// Even an unchanged complete inventory advances versions (a scan completion bump).
func (s *Store) ReconcileMissingSources(libraryID int64, paths map[string]struct{}) error {
	return s.writeVersioned(libraryID, func(tx *sql.Tx) error {
		missing, err := missingSourcePaths(tx, libraryID, paths)
		if err != nil {
			return err
		}
		return deleteSourcePathsTx(tx, libraryID, missing)
	})
}

// This interface allows legacy callers to enumerate before acquiring a writer,
// while versioned reconciliation enumerates within its reserved transaction.
type sourcePathQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

func missingSourcePaths(db sourcePathQuerier, libraryID int64, paths map[string]struct{}) ([]string, error) {
	rows, err := db.Query("SELECT source_path FROM movies WHERE library_id=?", libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if _, ok := paths[path]; !ok {
			missing = append(missing, path)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return missing, rows.Close()
}

func deleteSourcePathsTx(tx *sql.Tx, libraryID int64, missing []string) error {
	if err := InvalidateScanGroupsForPathsTx(tx, libraryID, missing); err != nil {
		return err
	}
	const chunk = 500
	for start := 0; start < len(missing); start += chunk {
		end := min(start+chunk, len(missing))
		batch := missing[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+1)
		args = append(args, libraryID)
		for _, path := range batch {
			args = append(args, path)
		}
		if _, err := tx.Exec("DELETE FROM movies WHERE library_id=? AND source_path IN ("+placeholders+")", args...); err != nil {
			return err
		}
	}
	return nil
}
