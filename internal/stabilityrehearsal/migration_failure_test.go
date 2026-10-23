package stabilityrehearsal

import (
	"strings"
	"testing"

	"emby-go/internal/store"
)

// This is a deterministic transaction failure, not a process-kill or power-loss
// simulation: failure at the v16 ledger insert must roll the whole pending
// migration transaction back to v13 and keep all user data available for retry.
func TestSchema13UpgradeLateFailureAndRetry(t *testing.T) {
	path, _ := legacyFixture(t)
	db := rawDB(t, path)
	mustExec(t, db, `CREATE TRIGGER stability_reject_v16 BEFORE INSERT ON schema_migrations
 WHEN NEW.version=16 BEGIN SELECT RAISE(ABORT,'stability injected migration ledger failure'); END`)
	s, err := store.Open(path)
	if s != nil {
		_ = s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "stability injected migration ledger failure") {
		t.Fatalf("expected injected migration failure, got %v", err)
	}
	if got := scalar(t, db, "SELECT max(version) FROM schema_migrations"); got != 13 {
		t.Fatalf("last committed version=%d, want 13", got)
	}
	if got := scalar(t, db, "SELECT count(*) FROM pragma_table_info('users') WHERE name='auth_generation'"); got != 0 {
		t.Fatal("failed upgrade left a partially migrated users table")
	}
	if got := scalar(t, db, "SELECT count(*) FROM sqlite_master WHERE name IN ('scan_groups','scan_group_sources')"); got != 0 {
		t.Fatalf("failed v16 left %d partial tables", got)
	}
	if got := scalar(t, db, "SELECT count(*) FROM movies WHERE id=42"); got != 1 {
		t.Fatal("media lost during failed upgrade")
	}
	if got := scalar(t, db, "SELECT position_ticks FROM userdata WHERE user_id=7 AND movie_id=42"); got != 1234567 {
		t.Fatal("watch state changed during failed upgrade")
	}
	mustExec(t, db, "DROP TRIGGER stability_reject_v16")
	recovered := openStore(t, path)
	retained(t, recovered)
	if got := scalar(t, db, "SELECT max(version) FROM schema_migrations"); got != 16 {
		t.Fatalf("retry version=%d", got)
	}
	integrity(t, path)
}
