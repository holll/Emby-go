package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
)

const extraBackdropsMigrationVersion = 17

// Freeze this statement: subsequent artwork changes need another migration.
const migration17Statement = `ALTER TABLE movies ADD COLUMN extra_backdrops TEXT NOT NULL DEFAULT '[]'`

func migration17Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(migration17Statement)))
}

func applyMigration17(tx *sql.Tx) error {
	if _, err := tx.Exec(migration17Statement); err != nil {
		return fmt.Errorf("schema migration %d: %w", extraBackdropsMigrationVersion, err)
	}
	return nil
}
