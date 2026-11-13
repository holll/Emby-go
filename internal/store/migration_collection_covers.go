package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

// Migration 23: persist the collection cover that was actually applied to disk.
//
// 合集封面由 server 侧拼贴生成后落盘；这张表只记录"已应用"的结果，供下次
// 生成前比对是否还能复用（source_hash/algorithm 变了才重做）。它不参与影片
// 索引，也不改 movies.collection，因此没有触发器、没有版本号联动。
const collectionCoverMigrationVersion = 23

// Freeze this statement; later column changes require a new migration.
var migration23Statements = []string{
	`CREATE TABLE IF NOT EXISTS collection_covers (
		collection TEXT PRIMARY KEY,
		path TEXT NOT NULL,
		source_hash TEXT NOT NULL,
		algorithm TEXT NOT NULL,
		width INTEGER NOT NULL,
		height INTEGER NOT NULL,
		applied_at TEXT NOT NULL
	)`,
}

func migration23Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration23Statements, "\n"))))
}

func applyMigration23(tx *sql.Tx) error {
	for _, statement := range migration23Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", collectionCoverMigrationVersion, err)
		}
	}
	return nil
}
