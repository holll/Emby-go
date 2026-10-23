package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

// scanStateMigrationVersion 16 adds persistent group fingerprints for
// incremental scans. It stores no movie content; rows only prove which disk
// inputs and index records the last successful group commit observed.
const scanStateMigrationVersion = 16

var migration16Statements = []string{
	`CREATE TABLE IF NOT EXISTS scan_groups (
		library_id INTEGER NOT NULL,
		group_key TEXT NOT NULL,
		fingerprint_version INTEGER NOT NULL,
		fingerprint TEXT NOT NULL,
		outcome TEXT NOT NULL,
		PRIMARY KEY (library_id, group_key),
		FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS scan_group_sources (
		library_id INTEGER NOT NULL,
		group_key TEXT NOT NULL,
		source_path TEXT NOT NULL,
		role TEXT NOT NULL,
		expected_movie_id INTEGER,
		PRIMARY KEY (library_id, group_key, source_path),
		FOREIGN KEY (library_id, group_key) REFERENCES scan_groups(library_id, group_key) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_scan_group_sources_path ON scan_group_sources(library_id, source_path)`,
}

func migration16Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration16Statements, "\n"))))
}

func applyMigration16(tx *sql.Tx) error {
	for _, statement := range migration16Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", scanStateMigrationVersion, err)
		}
	}
	return nil
}
