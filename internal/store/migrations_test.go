package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func migrationFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(baseSchema); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func execMigrationFixture(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationAdoptsLegacySchemaAndReopens(t *testing.T) {
	db, path := migrationFixture(t)
	// Synthetic legacy fixture, not evidence of support for every historic DB.
	for _, c := range baselineColumns {
		columns, err := tableColumns(db, c.table)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := columns[c.name]; exists {
			execMigrationFixture(t, db, "ALTER TABLE "+c.table+" DROP COLUMN "+c.name)
		}
	}
	execMigrationFixture(t, db, `INSERT INTO libraries(id,name,path) VALUES(1,'legacy','/legacy');
INSERT INTO movies(id,library_id,source_path,status,title,created_at,updated_at) VALUES(1,1,'/legacy/a.strm','success','legacy title','t0','t0');
INSERT INTO userdata(movie_id,play_count,position_ticks) VALUES(1,9,42);
INSERT INTO access_tokens(token,created_at) VALUES('fixture-not-a-real-secret','t0');
INSERT INTO api_keys(key,name,created_at) VALUES('fixture-key','legacy key','t0');
INSERT INTO administrators(id,username,password_hash,created_at) VALUES(1,'fixture-admin','fixture-hash','t0');
INSERT INTO actors(name,avatar_url,updated_at) VALUES('legacy actor','https://example.invalid/a.jpg','t0');
INSERT INTO movie_actors(movie_id,actor_name) VALUES(1,'legacy actor');
INSERT INTO settings(key,value) VALUES('fixture-setting','retained');
INSERT INTO scheduled_tasks(name,type,cron,params,created_at,updated_at) VALUES('legacy task','scan','0 8 * * *','{"library_id":1}','t0','t0')`)
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open pass %d: %v", i, err)
		}
		s.waitFeaturesReady()
		var count, position, stopped, favorite int64
		if err := s.db.QueryRow("SELECT play_count,position_ticks,last_stopped_ticks,is_favorite FROM userdata WHERE movie_id=1").Scan(&count, &position, &stopped, &favorite); err != nil {
			t.Fatal(err)
		}
		if count != 9 || position != 42 || stopped != -1 || favorite != 0 {
			t.Fatalf("lost legacy state: %d %d %d %d", count, position, stopped, favorite)
		}
		var checksum string
		if err := s.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=1").Scan(&checksum); err != nil {
			t.Fatal(err)
		}
		if checksum != baselineChecksum() {
			t.Fatalf("wrong checksum %q", checksum)
		}
		for query, want := range map[string]string{
			"SELECT title FROM movies WHERE id=1":                         "legacy title",
			"SELECT additional_parts FROM movies WHERE id=1":              "[]",
			"SELECT password_hash FROM administrators WHERE id=1":         "fixture-hash",
			"SELECT name FROM api_keys WHERE key_prefix='fixture-key'":    "legacy key",
			"SELECT actor_name FROM movie_actors WHERE movie_id=1":        "legacy actor",
			"SELECT avatar_url FROM actors WHERE name='legacy actor'":     "https://example.invalid/a.jpg",
			"SELECT value FROM settings WHERE key='fixture-setting'":      "retained",
			"SELECT params FROM scheduled_tasks WHERE name='legacy task'": `{"library_id":1}`,
		} {
			var got string
			if err := s.db.QueryRow(query).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s = %q, want %q", query, got, want)
			}
		}
		var tokens int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM access_tokens").Scan(&tokens); err != nil || tokens != 1 {
			t.Fatalf("credentials changed: count=%d err=%v", tokens, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationFailureRollsBackDDLAndLedger(t *testing.T) {
	for _, mode := range []string{"index failure", "validation failure"} {
		t.Run(mode, func(t *testing.T) {
			db, path := migrationFixture(t)
			execMigrationFixture(t, db, "ALTER TABLE userdata DROP COLUMN last_stopped_ticks")
			if mode == "index failure" {
				execMigrationFixture(t, db, "ALTER TABLE movies DROP COLUMN status")
			} else {
				execMigrationFixture(t, db, "ALTER TABLE userdata DROP COLUMN play_count")
			}
			if s, err := Open(path); err == nil {
				s.Close()
				t.Fatal("Open accepted broken schema")
			}
			cols, err := tableColumns(db, "userdata")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cols["last_stopped_ticks"]; ok {
				t.Fatal("ALTER committed despite migration failure")
			}
			var ledgers int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='schema_migrations'").Scan(&ledgers); err != nil {
				t.Fatal(err)
			}
			if ledgers != 0 {
				t.Fatal("failed migration left a ledger")
			}
		})
	}
}

func TestMigrationRejectsUnknownOrModifiedLedger(t *testing.T) {
	for _, statement := range []string{
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=1",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=2",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=3",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=4",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=5",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=6",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=7",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=8",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=9",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=11",
		"UPDATE schema_migrations SET checksum='tampered' WHERE version=12",
		"INSERT INTO schema_migrations VALUES(13,'future','t0')",
	} {
		t.Run(statement, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "check.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s.waitFeaturesReady()
			execMigrationFixture(t, s.db, statement)
			s.Close()
			if s, err := Open(path); err == nil {
				s.Close()
				t.Fatal("accepted unsupported/modified migration")
			} else if !strings.Contains(err.Error(), "unsupported or modified schema migration") {
				t.Fatal(err)
			}
		})
	}
}

func TestMigrationRejectsSchemaAndIndexDrift(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE userdata ADD COLUMN unexpected TEXT",
		"ALTER TABLE schema_migrations RENAME TO old_ledger; CREATE TABLE schema_migrations(version INTEGER, checksum TEXT NOT NULL, applied_at TEXT NOT NULL); INSERT INTO schema_migrations SELECT * FROM old_ledger; DROP TABLE old_ledger",
		"DROP INDEX idx_movies_status; CREATE INDEX idx_movies_status ON movies(title)",
		"DROP INDEX idx_movies_visible_title; DROP INDEX idx_movies_visible_library_title; ALTER TABLE movies DROP COLUMN title",
		"ALTER TABLE movies ADD COLUMN generated_title TEXT GENERATED ALWAYS AS (title) VIRTUAL",
		"CREATE UNIQUE INDEX unexpected_unique_title ON movies(title)",
		"DROP INDEX idx_movies_status; CREATE INDEX idx_movies_status ON movies(status DESC)",
		"DROP INDEX idx_movies_status; CREATE INDEX idx_movies_status ON movies(status COLLATE NOCASE)",
		"DROP INDEX idx_movies_status; CREATE INDEX idx_movies_status ON movies(status) WHERE status='success'",
		"DROP INDEX idx_movies_library_status; CREATE INDEX idx_movies_library_status ON movies(status,library_id)",
	} {
		t.Run(statement, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "check.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s.waitFeaturesReady()
			execMigrationFixture(t, s.db, statement)
			s.Close()
			if s, err := Open(path); err == nil {
				s.Close()
				t.Fatal("accepted schema drift")
			} else if !strings.Contains(err.Error(), "schema drift") {
				t.Fatal(err)
			}
		})
	}
}

func TestMigrationFailsOnReadOnlyConnection(t *testing.T) {
	db, _ := migrationFixture(t)
	db.SetMaxOpenConns(1)
	execMigrationFixture(t, db, "PRAGMA query_only=ON")
	if err := (&Store{db: db}).migrate(); err == nil {
		t.Fatal("accepted read-only migration")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='schema_migrations'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration recorded")
	}
}

func TestMigrationChecksumIsFrozen(t *testing.T) {
	const want = "9d69f4dc7203e14d0c93bc1bfb9bb2875fa34bde2578c56f2da2c7a62426e100"
	if got := baselineChecksum(); got != want {
		t.Fatalf("migration 1 changed: %s; append a migration instead", got)
	}
}

func TestMigrationRejectsMissingConstraints(t *testing.T) {
	for _, replacement := range [][2]string{
		{"source_path TEXT UNIQUE NOT NULL", "source_path TEXT NOT NULL"},
		{"path TEXT UNIQUE NOT NULL", "path TEXT NOT NULL"},
		{"username TEXT NOT NULL UNIQUE", "username TEXT NOT NULL"},
		{"CHECK(id=1)", "/* CHECK(id=1) is not a constraint */"},
		{"CHECK(id=1)", "CHECK(id>=1)"},
		{"PRIMARY KEY AUTOINCREMENT", "PRIMARY KEY"},
	} {
		t.Run(replacement[0]+replacement[1], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			execMigrationFixture(t, db, strings.Replace(baseSchema, replacement[0], replacement[1], 1))
			if s, err := Open(path); err == nil {
				s.Close()
				t.Fatal("accepted missing/mutated constraint")
			} else if !strings.Contains(err.Error(), "schema drift") {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='schema_migrations'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("invalid schema received a migration ledger")
			}
		})
	}
}

func TestMigrationAcceptsEquivalentIndexSpelling(t *testing.T) {
	_, path := migrationFixture(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.waitFeaturesReady()
	execMigrationFixture(t, s.db, `DROP INDEX idx_movies_status;
create index if not exists "idx_movies_status" on "movies"("status" ASC)`)
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.waitFeaturesReady()
	s.Close()
}

func TestMigrationDoesNotDeleteHistoricalMovies(t *testing.T) {
	db, path := migrationFixture(t)
	execMigrationFixture(t, db, `INSERT INTO libraries(id,name,path) VALUES(1,'old','/old');
INSERT INTO movies(library_id,source_path,status,created_at,updated_at) VALUES(1,'/old/a.strm','incompatible','t0','t0')`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.waitFeaturesReady()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movies WHERE status='incompatible'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("startup deleted historical records without an approved cleanup")
	}
}
