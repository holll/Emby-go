package store

import "database/sql"

// DeleteIndexedMovie removes an indexed item and advances its actual library's
// version in the same transaction. No match returns sql.ErrNoRows, without a bump.
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
// DeleteLibrary now shares this versioned contract (P2-10), so both names point
// at one implementation instead of a versioned and an unversioned copy.
func (s *Store) DeleteIndexedLibrary(id int64) error {
	return s.DeleteLibrary(id)
}
