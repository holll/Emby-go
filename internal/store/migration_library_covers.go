package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

// Migration 24: persist the media-library cover that was actually applied to disk.
//
// 与 collection_covers 同构，但主键是库 id：库名可以改，id 不会。表只记录
// "已应用"的结果，供读取封面时判断是否还有效（文件被删就回退代表图），
// 不参与影片索引，也没有触发器与版本号联动。
const libraryCoverMigrationVersion = 24

// Freeze this statement; later column changes require a new migration.
var migration24Statements = []string{
	`CREATE TABLE IF NOT EXISTS library_covers (
		library_id INTEGER PRIMARY KEY,
		path TEXT NOT NULL,
		source_hash TEXT NOT NULL,
		algorithm TEXT NOT NULL,
		width INTEGER NOT NULL,
		height INTEGER NOT NULL,
		applied_at TEXT NOT NULL
	)`,
}

func migration24Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration24Statements, "\n"))))
}

func applyMigration24(tx *sql.Tx) error {
	for _, statement := range migration24Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", libraryCoverMigrationVersion, err)
		}
	}
	return nil
}
