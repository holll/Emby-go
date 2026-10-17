package store

import (
	"database/sql"
	"errors"
	"time"
)

// SetIndexedImage changes only an image path and updated_at, never a stale Movie
// snapshot. The actual row's library version commits with the update. Filesystem
// writes are outside this transaction and must be reconciled separately on error.
func (s *Store) SetIndexedImage(id int64, kind, path string) error {
	var column string
	switch kind {
	case "poster", "primary":
		column = "poster_path"
	case "backdrop", "fanart":
		column = "backdrop_path"
	case "landscape":
		column = "landscape_path"
	default:
		return errors.New("unsupported image kind")
	}
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		var libraryID sql.NullInt64
		// The column is a code-owned whitelist, not caller-controlled SQL.
		if err := tx.QueryRow("UPDATE movies SET "+column+"=?,updated_at=? WHERE id=? RETURNING library_id",
			path, time.Now().UTC().Format(time.RFC3339), id).Scan(&libraryID); err != nil {
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
