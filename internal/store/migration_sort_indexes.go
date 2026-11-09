package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

// Migration 21: visible list sorting. The query always applies the partial
// predicate status IN ('success','manual'), so each sort now has its own
// partial index instead of only title.
const visibleSortIndexMigrationVersion = 21

var migration21Statements = []string{
	`CREATE INDEX IF NOT EXISTS idx_movies_visible_year ON movies(year, id) WHERE status IN ('success','manual')`,
	`CREATE INDEX IF NOT EXISTS idx_movies_visible_premiered ON movies(premiered, id) WHERE status IN ('success','manual')`,
	`CREATE INDEX IF NOT EXISTS idx_movies_visible_created_at ON movies(created_at, id) WHERE status IN ('success','manual')`,
	`CREATE INDEX IF NOT EXISTS idx_movies_visible_rating ON movies(rating, id) WHERE status IN ('success','manual')`,
	`CREATE INDEX IF NOT EXISTS idx_movies_visible_sortname ON movies(COALESCE(NULLIF(sortname, ''), title), id) WHERE status IN ('success','manual')`,
	`ANALYZE`,
}

func migration21Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration21Statements, "\n"))))
}

func applyMigration21(tx *sql.Tx) error {
	for _, statement := range migration21Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", visibleSortIndexMigrationVersion, err)
		}
	}
	return nil
}
