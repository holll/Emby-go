//go:build linux

package stabilityrehearsal

import (
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"emby-go/internal/store"
	"modernc.org/sqlite"
)

// Unlike max_page_count, this fills a real, bounded filesystem. Mount changes
// exist only in the child namespace; no host filesystem is filled.
func TestSchema13MigrationDiskFullAndRetry(t *testing.T) {
	if os.Getenv("EMBY_GO_TEST_TMPFS_ENOSPC") != "1" {
		t.Skip("set EMBY_GO_TEST_TMPFS_ENOSPC=1 with mount namespace/tmpfs privileges")
	}
	const parentEnv = "EMBY_MIGRATION_ENOSPC_PARENT_NS"
	parentNS := os.Getenv(parentEnv)
	currentNS, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if parentNS == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSchema13MigrationDiskFullAndRetry$", "-test.v", "-test.timeout=60s")
		cmd.Env = append(os.Environ(), parentEnv+"="+currentNS)
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
		output, err := cmd.CombinedOutput()
		t.Logf("isolated migration ENOSPC child:\n%s", output)
		if err != nil {
			t.Fatalf("isolated migration child: %v", err)
		}
		return
	}
	if currentNS == parentNS {
		t.Fatal("migration child did not enter a separate mount namespace")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	volume := t.TempDir()
	if err := syscall.Mount("tmpfs", volume, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "size=8m,mode=0700"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Unmount(volume, 0); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(volume, "migration.sqlite")
	if !t.Run("prepare-and-close", func(t *testing.T) {
		legacy, _ := legacyFixture(t)
		mustExec(t, rawDB(t, legacy), "VACUUM INTO ?", path)
	}) {
		return
	}
	db := rawDB(t, path)
	// v13 credentials already contain digests. Include both live and deleted
	// account references: v14 removes only orphan tokens, while v15 backfills
	// every retained key's stable identity. These are synthetic test digests.
	liveToken, orphanToken := strings.Repeat("a", 64), strings.Repeat("b", 64)
	liveKey, orphanKey := strings.Repeat("c", 64), strings.Repeat("d", 64)
	mustExec(t, db, `INSERT INTO access_tokens(token,created_at,expires_at,user_id) VALUES
 (?, '2026-01-01T00:00:00Z',4102444800,7),
 (?, '2026-01-02T00:00:00Z',4102444801,700)`, liveToken, orphanToken)
	mustExec(t, db, `INSERT INTO api_keys(key,name,created_at,key_hash,key_prefix,user_id) VALUES
 (?, 'live-key','2026-01-01T00:00:00Z',?,'live-prefix',7),
 (?, 'orphan-key','2026-01-02T00:00:00Z',?,'orphan-prefix',701)`, liveKey, liveKey, orphanKey, orphanKey)
	mustExec(t, db, `INSERT INTO login_events(user_id,name,ip,success,created_at) VALUES
 (7,'viewer','192.0.2.7',1,'2026-01-01T00:00:00Z'),
 (702,'deleted-viewer','192.0.2.8',0,'2026-01-02T00:00:00Z')`)
	// The synchronous trigger runs after all pending DDL but before ledger18
	// insertion/commit. It returns success: SQLite itself must fail its writes.
	mustExec(t, db, `CREATE TRIGGER stability_fill_v20 BEFORE INSERT ON schema_migrations
WHEN NEW.version=18 BEGIN
 SELECT CASE WHEN
  (SELECT count(*) FROM pragma_table_info('movies') WHERE name IN ('extra_backdrops','auto_marks'))=2
  AND (SELECT count(*) FROM schema_migrations WHERE version=17)=1
  AND (SELECT count(*) FROM access_tokens)=1
  AND (SELECT count(*) FROM access_tokens WHERE user_id=7)=1
  AND (SELECT count(*) FROM pragma_table_info('access_tokens') WHERE name='auth_generation')=1
  AND (SELECT count(*) FROM api_keys)=2
  AND (SELECT count(*) FROM pragma_table_info('api_keys') WHERE name IN ('stable_id','version','rotated_at'))=3
  AND (SELECT count(*) FROM login_events)=2
  AND (SELECT seq FROM sqlite_sequence WHERE name='users')=702
 THEN stability_fill_volume() ELSE RAISE(ABORT,'missing precommit migration work') END;
END`)
	before := capacitySnapshot(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	filler := filepath.Join(volume, "enospc-fill")
	filled := false
	if err := sqlite.RegisterScalarFunction("stability_fill_volume", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		f, err := os.OpenFile(filler, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		block := make([]byte, 4096)
		for i := 0; i < 4096; i++ { // At most 16 MiB, only on the 8 MiB mount.
			if _, err = f.Write(block); err != nil {
				break
			}
		}
		closeErr := f.Close()
		if !errors.Is(err, syscall.ENOSPC) {
			t.Errorf("filler must reach kernel ENOSPC, got %v", err)
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		filled = true
		t.Log("ledger18 trigger reached kernel ENOSPC on private 8 MiB tmpfs")
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	s, openErr := store.Open(path)
	if s != nil {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}
	if !filled {
		t.Fatalf("migration did not reach the filesystem-filling trigger: %v", openErr)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(openErr, &sqliteErr) || sqliteErr.Code() != 13 {
		t.Fatalf("want real filesystem SQLITE_FULL code13, got %v", openErr)
	}
	if !strings.HasPrefix(openErr.Error(), "commit schema migration:") {
		t.Fatalf("capacity failure must occur at transaction commit, got %v", openErr)
	}
	t.Logf("migration disk-full error: %v", openErr)
	if err := os.Remove(filler); err != nil {
		t.Fatal(err)
	}
	if !t.Run("raw-rollback-before-store-reopen", func(t *testing.T) {
		db := rawDB(t, path)
		if after := capacitySnapshot(t, db); !reflect.DeepEqual(before, after) {
			for name, want := range before {
				if !reflect.DeepEqual(want, after[name]) {
					t.Errorf("%s changed after OS ENOSPC: got=%#v want=%#v", name, after[name], want)
				}
			}
		}
		if scalar(t, db, "SELECT count(*) FROM schema_migrations") != 13 || scalar(t, db, "SELECT max(version) FROM schema_migrations") != 13 {
			t.Fatal("disk-full failure left a partial migration ledger")
		}
		integrity(t, path)
		// Remove only the fixture trigger, after proving rollback including DDL.
		mustExec(t, db, "DROP TRIGGER stability_fill_v20")
	}) {
		return
	}
	t.Run("space-released-explicit-retry", func(t *testing.T) {
		s := openStore(t, path)
		retained(t, s)
		db := rawDB(t, path)
		if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 20 || scalar(t, db, "SELECT count(*) FROM schema_migrations") != 20 {
			t.Fatal("retry did not reach complete schema20")
		}
		if scalar(t, db, "SELECT count(*) FROM playback_sessions") != 2 || scalar(t, db, "SELECT count(*) FROM playback_events") != 2 || scalar(t, db, "SELECT count(*) FROM movies WHERE id=42 AND auto_marks='[]'") != 1 {
			t.Fatal("retry lost retained history or schema20 defaults")
		}
		var liveTokens, keptKeys int
		if err := db.QueryRow(`SELECT count(*) FROM access_tokens WHERE token=? AND user_id=7
 AND created_at='2026-01-01T00:00:00Z' AND expires_at=4102444800 AND auth_generation=1`, liveToken).Scan(&liveTokens); err != nil {
			t.Fatal(err)
		}
		if liveTokens != 1 || scalar(t, db, "SELECT count(*) FROM access_tokens") != 1 {
			t.Fatal("retry must retain the live token and remove only the orphan token")
		}
		if err := db.QueryRow(`SELECT count(*) FROM api_keys WHERE length(stable_id)=32 AND version=1 AND rotated_at IS NULL AND
 ((key=? AND key_hash=? AND user_id=7 AND name='live-key' AND key_prefix='live-prefix' AND created_at='2026-01-01T00:00:00Z') OR
 (key=? AND key_hash=? AND user_id=701 AND name='orphan-key' AND key_prefix='orphan-prefix' AND created_at='2026-01-02T00:00:00Z'))`, liveKey, liveKey, orphanKey, orphanKey).Scan(&keptKeys); err != nil {
			t.Fatal(err)
		}
		if keptKeys != 2 || scalar(t, db, "SELECT count(*) FROM api_keys") != 2 || scalar(t, db, "SELECT count(DISTINCT stable_id) FROM api_keys") != 2 {
			t.Fatal("retry lost key fields or failed unique stable identity backfill")
		}
		if after := capacitySnapshot(t, db); !reflect.DeepEqual(before["login_events"], after["login_events"]) {
			t.Fatal("retry changed retained login history")
		}
		if scalar(t, db, "SELECT seq FROM sqlite_sequence WHERE name='users'") != 702 {
			t.Fatal("retry did not preserve the highest deleted identity from login history")
		}
		integrity(t, path)
	})
	// Scope is OS ENOSPC at the transaction's write/commit boundary, not
	// physical disk durability, power loss, or every intermediate migration.
}
