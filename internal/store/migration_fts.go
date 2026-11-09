package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

// Migration 22: replace leading-% LIKE and JSON LIKE with structured indexes.
//
//   - movies_fts: standalone (owned-rowid) FTS5 trigram table over
//     title/original_title/number. Trigram gives substring semantics for
//     queries of length >= 3; shorter queries cannot form a trigram and
//     keep the LIKE fallback in search(). Content is owned by the FTS
//     table itself (not an external-content view), so rebuild is the
//     plain DELETE + INSERT..SELECT below and stays a within-migration
//     operation for a single writer.
//
//     IMPORTANT: triggers reference NEW.title/NEW.original_title/
//     NEW.number. ALTER TABLE movies DROP COLUMN title (used by the
//     drift test) fails on a database that already has these triggers,
//     because SQLite revalidates trigger bodies on DROP COLUMN. Any
//     future DROP COLUMN migration of those columns must drop the
//     triggers first (drop movies_fts_*) and recreate them.
const searchFtsMigrationVersion = 22

var migration22Statements = []string{
	`CREATE VIRTUAL TABLE IF NOT EXISTS movies_fts USING fts5(title, original_title, number, tokenize='trigram')`,
	`CREATE TRIGGER IF NOT EXISTS movies_fts_insert AFTER INSERT ON movies
		BEGIN
			INSERT INTO movies_fts(rowid, title, original_title, number)
			VALUES (new.id, new.title, new.original_title, new.number);
		END`,
	`CREATE TRIGGER IF NOT EXISTS movies_fts_update AFTER UPDATE OF title, original_title, number ON movies
		BEGIN
			DELETE FROM movies_fts WHERE rowid=old.id;
			INSERT INTO movies_fts(rowid, title, original_title, number)
			VALUES (new.id, new.title, new.original_title, new.number);
		END`,
	`CREATE TRIGGER IF NOT EXISTS movies_fts_delete AFTER DELETE ON movies
		BEGIN
			DELETE FROM movies_fts WHERE rowid=old.id;
		END`,
	`DELETE FROM movies_fts`,
	`INSERT INTO movies_fts(rowid, title, original_title, number)
		SELECT id, title, original_title, number FROM movies`,
	`ANALYZE`,
}

func migration22Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration22Statements, "\n"))))
}

func applyMigration22(tx *sql.Tx) error {
	for _, statement := range migration22Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", searchFtsMigrationVersion, err)
		}
	}
	return nil
}
