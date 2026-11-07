package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const autoMarksMigrationVersion = 18

// Freeze this statement; later provenance changes require a new migration.
const migration18Statement = `ALTER TABLE movies ADD COLUMN auto_marks TEXT NOT NULL DEFAULT '[]'`

func migration18Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(migration18Statement)))
}

func applyMigration18(tx *sql.Tx) error {
	if _, err := tx.Exec(migration18Statement); err != nil {
		return fmt.Errorf("schema migration %d: %w", autoMarksMigrationVersion, err)
	}
	return nil
}

// SetAutoMarks records scraper-owned provenance without changing tags or the
// scrape outcome. Ordinary indexing must never infer or replace this ownership.
func (s *Store) SetAutoMarks(movieID int64, marks []string) error {
	raw := "[]"
	if len(marks) > 0 {
		raw = jsonText(marks)
	}
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		var libraryID sql.NullInt64
		if err := tx.QueryRow("UPDATE movies SET auto_marks=? WHERE id=? RETURNING library_id", raw, movieID).Scan(&libraryID); err != nil {
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
