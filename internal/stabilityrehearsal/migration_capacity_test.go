package stabilityrehearsal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
	"modernc.org/sqlite"
)

// max_page_count exercises SQLite's own allocator, not an injected RAISE and
// not a full host filesystem. The driver-wide hook exists only in this child.
func TestMigrationCapacityChild(t *testing.T) {
	path := os.Getenv("EMBY_MIGRATION_CAPACITY_DB")
	if path == "" {
		t.Skip("capacity-limited subprocess only")
	}
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		readPragma := func(query string) (int64, error) {
			rows, err := conn.QueryContext(context.Background(), query, nil)
			if err != nil {
				return 0, err
			}
			defer rows.Close()
			value := make([]driver.Value, 1)
			if err := rows.Next(value); err != nil {
				return 0, err
			}
			return value[0].(int64), nil
		}
		pages, err := readPragma("PRAGMA page_count")
		if err != nil {
			return err
		}
		limit, err := readPragma(fmt.Sprintf("PRAGMA max_page_count=%d", pages))
		if err != nil {
			return err
		}
		if limit != pages {
			return fmt.Errorf("page limit=%d, current pages=%d", limit, pages)
		}
		t.Logf("SQLite connection page_count=max_page_count=%d", pages)
		return nil
	})
	s, err := store.Open(path)
	if s != nil {
		if closeErr := s.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 13 {
		t.Fatalf("want engine SQLITE_FULL code 13 during migration, got %v", err)
	}
	if !strings.Contains(err.Error(), "schema migration") {
		t.Fatalf("not a migration capacity failure: %v", err)
	}
	t.Logf("engine failure code=%d: %v", sqliteErr.Code(), err)
}

// Capture complete historical rows and schema, rather than merely checking that
// the version number survived. All fixture tables here have ordinary rowids.
func capacitySnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	queries := map[string]string{"schema": "SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name"}
	for _, table := range []string{"schema_migrations", "users", "libraries", "movies", "user_libraries", "userdata", "playback_sessions", "playback_events", "admin_audit_events", "access_tokens", "api_keys", "login_events", "sqlite_sequence", "kv"} {
		queries[table] = "SELECT * FROM " + table + " ORDER BY rowid"
	}
	snapshot := make(map[string][][]any, len(queries))
	for name, query := range queries {
		snapshot[name] = nil
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			args := make([]any, len(columns))
			for i := range values {
				args[i] = &values[i]
			}
			if err := rows.Scan(args...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot[name] = append(snapshot[name], values)
		}
		err = rows.Err()
		if closeErr := rows.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestSchema13UpgradeCapacityFailureAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.sqlite")
	// The fixture's subtest cleanup closes every preparation handle before the
	// child starts. VACUUM INTO produces a consistent, independently owned DB.
	t.Run("prepare", func(t *testing.T) {
		legacy, _ := legacyFixture(t)
		mustExec(t, rawDB(t, legacy), "VACUUM INTO ?", path)
	})
	var before map[string][][]any
	t.Run("before", func(t *testing.T) {
		before = capacitySnapshot(t, rawDB(t, path))
	})
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestMigrationCapacityChild$", "-test.v")
	cmd.Env = append(os.Environ(), "EMBY_MIGRATION_CAPACITY_DB="+path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capacity subprocess: %v (context=%v)\n%s", err, ctx.Err(), output)
	}
	t.Logf("capacity subprocess:\n%s", output)
	if !strings.Contains(string(output), "engine failure code=13:") {
		t.Fatal("child did not report a real SQLITE_FULL")
	}
	t.Run("raw_rollback", func(t *testing.T) {
		db := rawDB(t, path)
		after := capacitySnapshot(t, db)
		for name, want := range before {
			if !reflect.DeepEqual(after[name], want) {
				t.Errorf("%s changed after SQLITE_FULL: got=%#v want=%#v", name, after[name], want)
			}
		}
		if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 13 || scalar(t, db, "SELECT count(*) FROM schema_migrations") != 13 {
			t.Fatal("capacity failure left a partial ledger")
		}
		if scalar(t, db, "SELECT count(*) FROM pragma_table_info('users') WHERE name='auth_generation'") != 0 || scalar(t, db, "SELECT count(*) FROM sqlite_master WHERE name IN ('scan_groups','scan_group_sources','users_new')") != 0 {
			t.Fatal("capacity failure left partial migrated schema")
		}
		integrity(t, path)
	})
	// The hook is process-local: the parent's fresh connection has no page cap.
	t.Run("unrestricted_retry", func(t *testing.T) {
		s := openStore(t, path)
		retained(t, s)
		db := rawDB(t, path)
		if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 20 || scalar(t, db, "SELECT count(*) FROM schema_migrations") != 20 {
			t.Fatal("unrestricted retry did not reach complete v20")
		}
		if scalar(t, db, "SELECT count(*) FROM playback_sessions") != 2 || scalar(t, db, "SELECT count(*) FROM playback_events") != 2 || scalar(t, db, "SELECT count(*) FROM movies WHERE id=42 AND auto_marks='[]'") != 1 {
			t.Fatal("retry lost retained history or v20 defaults")
		}
		integrity(t, path)
	})
}
