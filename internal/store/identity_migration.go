package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

const identityMigrationVersion = 14

// Freeze these statements: later identity changes need another migration.
// user_libraries is staged before rebuilding its parent so DROP cannot cascade
// away existing grants. Audit rows deliberately retain their original identities.
var migration14Statements = []string{
	`CREATE TABLE user_libraries_identity_stage AS SELECT user_id,library_id FROM user_libraries`,
	`DROP TABLE user_libraries`,
	`CREATE TABLE users_identity_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  auth_generation INTEGER NOT NULL DEFAULT 1
 )`,
	`INSERT INTO users_identity_new(id,name,password_hash,is_admin,created_at,updated_at)
 SELECT id,name,password_hash,is_admin,created_at,updated_at FROM users`,
	`DROP TABLE users`,
	`ALTER TABLE users_identity_new RENAME TO users`,
	`CREATE TABLE user_libraries (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  PRIMARY KEY(user_id,library_id)
 )`,
	`CREATE INDEX idx_user_libraries_library ON user_libraries(library_id)`,
	`INSERT INTO user_libraries(user_id,library_id) SELECT user_id,library_id FROM user_libraries_identity_stage`,
	`DROP TABLE user_libraries_identity_stage`,
	// Include deleted accounts still represented by retained records, not just the
	// current users maximum. sqlite_sequence then survives future deletions/reopens.
	`DELETE FROM sqlite_sequence WHERE name='users'`,
	`INSERT INTO sqlite_sequence(name,seq) SELECT 'users',COALESCE(MAX(user_id),0) FROM (
  SELECT id AS user_id FROM users UNION ALL SELECT user_id FROM access_tokens
  UNION ALL SELECT user_id FROM api_keys UNION ALL SELECT user_id FROM userdata
  UNION ALL SELECT user_id FROM playback_sessions UNION ALL SELECT user_id FROM playback_events
  UNION ALL SELECT user_id FROM login_events UNION ALL SELECT user_id FROM admin_audit_events
  UNION ALL SELECT user_id FROM user_libraries
 )`,
	`ALTER TABLE access_tokens ADD COLUMN auth_generation INTEGER NOT NULL DEFAULT 1`,
	`DELETE FROM access_tokens WHERE NOT EXISTS (SELECT 1 FROM users WHERE users.id=access_tokens.user_id)`,
}

func migration14Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration14Statements, "\n"))))
}

func applyMigration14(tx *sql.Tx) error {
	for _, statement := range migration14Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", identityMigrationVersion, err)
		}
	}
	return nil
}
