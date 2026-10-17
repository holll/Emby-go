package store

import (
	"database/sql"
	"time"
)

// MarkIndexedManual records a saved manual NFO and its actual library version
// together. It does not read/write the file or rebuild metadata/actors/features.
// Even an already-manual row advances versions: the NFO's locks may have changed.
// Legacy MarkManual retains its unversioned contract.
func (s *Store) MarkIndexedManual(sourcePath, nfoPath string) error {
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		var libraryID sql.NullInt64
		if err := tx.QueryRow("UPDATE movies SET status='manual', nfo_path=?, updated_at=? WHERE source_path=? RETURNING library_id",
			nfoPath, time.Now().UTC().Format(time.RFC3339), sourcePath).Scan(&libraryID); err != nil {
			return err
		}
		if libraryID.Valid {
			return bumpLibraryVersionTx(tx, libraryID.Int64)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publishVersion(version)
	return nil
}
