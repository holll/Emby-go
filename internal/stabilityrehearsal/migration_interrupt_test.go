//go:build linux

package stabilityrehearsal

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"emby-go/internal/store"

	"modernc.org/sqlite"
)

// Only the isolated child registers this function; no production connection or
// parent test process gains a migration hook. The stdin pipe blocks inside the
// ledger18 trigger until the parent delivers SIGKILL, not a graceful rollback.
func TestMigrationInterruptChild(t *testing.T) {
	path := os.Getenv("EMBY_MIGRATION_INTERRUPT_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	if err := sqlite.RegisterScalarFunction("stability_pause_migration", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		if err := os.WriteFile(os.Getenv("EMBY_MIGRATION_INTERRUPT_MARKER"), []byte("ledger18-uncommitted"), 0600); err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintln(os.Stdout, "ledger18-uncommitted"); err != nil {
			return nil, err
		}
		var release [1]byte
		_, err := io.ReadFull(os.Stdin, release[:])
		return nil, fmt.Errorf("migration barrier unexpectedly released: %v", err)
	}); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if s != nil {
		_ = s.Close()
	}
	t.Fatalf("migration returned without SIGKILL: %v", err)
}

// SIGKILL tests process-crash recovery, not real power loss, disk exhaustion or
// filesystem durability. Every parent DB handle is closed before child start.
func TestSchema13MigrationSIGKILLAndRetry(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "interrupted.sqlite")
	if !t.Run("prepare-and-close-legacy-fixture", func(t *testing.T) {
		// Use the parent's directory so the fixture survives subtest Cleanup,
		// which closes legacyFixture's hidden rawDB before the child starts.
		legacy, media := legacyFixture(t)
		if err := os.Mkdir(filepath.Join(root, "media"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"A.strm", "A.nfo"} {
			body, err := os.ReadFile(filepath.Join(media, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "media", name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		body, err := os.ReadFile(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	// Snapshot complete rows and column names, not just selected counters. These
	// literal fixture tables all have rowid; sqlite_master also captures DDL.
	capture := func(t *testing.T, db *sql.DB) map[string]string {
		t.Helper()
		out := make(map[string]string)
		for _, table := range []string{"sqlite_master", "sqlite_sequence", "schema_migrations", "users", "user_libraries", "libraries", "movies", "userdata", "playback_sessions", "playback_events", "admin_audit_events", "kv"} {
			rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(cols)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot bytes.Buffer
			snapshot.Write(body)
			for rows.Next() {
				values := make([]any, len(cols))
				dest := make([]any, len(cols))
				for i := range values {
					dest[i] = &values[i]
				}
				if err := rows.Scan(dest...); err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				snapshot.WriteByte('\n')
				snapshot.Write(body)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			out[table] = snapshot.String()
		}
		return out
	}
	db := rawDB(t, path)
	media := filepath.Join(root, "media")
	mustExec(t, db, "UPDATE libraries SET path=? WHERE id=11", media)
	mustExec(t, db, "UPDATE movies SET source_path=?,nfo_path=?,output_dir=? WHERE id=42", filepath.Join(media, "A.strm"), filepath.Join(media, "A.nfo"), media)
	// All pending schema work precedes ledger18 insertion in migrate(). Also
	// assert the schema17/18 columns and ledger17 in the actual transaction.
	mustExec(t, db, `CREATE TRIGGER stability_pause_v20 BEFORE INSERT ON schema_migrations
WHEN NEW.version=18 BEGIN
 SELECT CASE WHEN
  (SELECT count(*) FROM pragma_table_info('movies') WHERE name IN ('extra_backdrops','auto_marks'))=2
  AND (SELECT count(*) FROM schema_migrations WHERE version=17)=1
 THEN stability_pause_migration() ELSE RAISE(ABORT,'missing precommit schema17/18 work') END;
END`)
	before := capture(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(root, "ledger18.marker")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// This deadline bounds harness failure, not migration timing. Readiness is
	// the trigger's synchronous pipe message, never a sleep or timer guess.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestMigrationInterruptChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "EMBY_MIGRATION_INTERRUPT_DB="+path, "EMBY_MIGRATION_INTERRUPT_MARKER="+marker)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// After Start every path reaps the child exactly once, before rawDB opens.
	line, readErr := bufio.NewReader(stdout).ReadString('\n')
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	if readErr != nil || line != "ledger18-uncommitted\n" {
		t.Fatalf("migration did not reach trigger: line=%q read=%v wait=%v stderr=%s", line, readErr, waitErr, stderr.String())
	}
	if killErr != nil {
		t.Fatal(killErr)
	}
	if waitErr == nil {
		t.Fatal("child unexpectedly exited successfully")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child was not killed by SIGKILL: %v", cmd.ProcessState)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "ledger18-uncommitted" {
		t.Fatalf("transaction marker=%q err=%v", body, err)
	}

	// No Store.Open/scan may mask crash recovery before this raw comparison.
	db = rawDB(t, path)
	if after := capture(t, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("SIGKILL left partial schema/business state:\ngot=%v\nwant=%v", after, before)
	}
	if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 13 ||
		scalar(t, db, "SELECT count(*) FROM pragma_table_info('users') WHERE name='auth_generation'") != 0 ||
		scalar(t, db, "SELECT count(*) FROM pragma_table_info('movies') WHERE name IN ('extra_backdrops','auto_marks')") != 0 ||
		scalar(t, db, "SELECT count(*) FROM sqlite_master WHERE name IN ('scan_groups','scan_group_sources')") != 0 {
		t.Fatal("interrupted migration left committed partial schema")
	}
	mustExec(t, db, "DROP TRIGGER stability_pause_v20")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recovered := openStore(t, path)
	retained(t, recovered)
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	db = rawDB(t, path)
	if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 20 {
		t.Fatal("retry did not migrate to schema20")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	integrity(t, path)
}
