package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newProbeTxStore(t *testing.T) *Store {
	t.Helper()
	s := versionStore(t, filepath.Join(t.TempDir(), "probe-tx.db"))
	// Include multiple existing domains and leave others absent. Probe must not
	// increment existing versions, create a new domain, or publish to the mirror.
	for _, id := range []int64{0, 7, 9} {
		if err := s.BumpVersion(id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func seedProbeTxRows(t *testing.T, s *Store, count int) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for id := 1; id <= count; id++ {
		// Future timestamps deliberately disagree with insertion order: retention
		// and listing must use IDs, not wall-clock timestamps.
		if _, err := tx.Exec(`INSERT INTO api_probe(id,method,path,query,body_preview,created_at) VALUES(?,'GET',?,'seed=1','seed-body','2099-01-01T00:00:00Z')`, id, fmt.Sprintf("/seed/%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func probeTxVersions(t *testing.T, s *Store) string {
	t.Helper()
	return libraryDeleteRows(t, s, "SELECT key,value FROM kv ORDER BY key") + "\nmirror=" + s.Version("g:version")
}

func probeTxRows(t *testing.T, s *Store) string {
	t.Helper()
	return libraryDeleteRows(t, s, "SELECT * FROM api_probe ORDER BY id")
}

func assertProbeTxUnchanged(t *testing.T, s *Store, rows, versions string) {
	t.Helper()
	if got := probeTxRows(t, s); got != rows {
		t.Fatalf("failed Probe changed original probe rows\nbefore=%s\nafter=%s", rows, got)
	}
	if got := probeTxVersions(t, s); got != versions {
		t.Fatalf("Probe changed versions/kv/mirror\nbefore=%s\nafter=%s", versions, got)
	}
}

func TestProbeTxRetentionOrderAndNoVersionBump(t *testing.T) {
	for _, existingVersions := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_versions=%t", existingVersions), func(t *testing.T) {
			s := versionStore(t, filepath.Join(t.TempDir(), "retention.db"))
			if existingVersions {
				for _, id := range []int64{0, 7, 9} {
					if err := s.BumpVersion(id); err != nil {
						t.Fatal(err)
					}
				}
			}
			seedProbeTxRows(t, s, 999)
			versions := probeTxVersions(t, s)
			for newest := int64(1000); newest <= 1003; newest++ {
				path := fmt.Sprintf("/probe/%d", newest)
				started := time.Now().UTC().Truncate(time.Second)
				if err := s.Probe("POST", path, "q='中文'&x=1", "{\"body\":\"探针\"}"); err != nil {
					t.Fatal(err)
				}
				probes, err := s.Probes(2000)
				if err != nil {
					t.Fatal(err)
				}
				if len(probes) != 1000 {
					t.Fatalf("after id %d retained %d rows, want 1000", newest, len(probes))
				}
				for i, probe := range probes {
					if got := probe["id"]; got != newest-int64(i) {
						t.Fatalf("row %d id=%v want %d", i, got, newest-int64(i))
					}
				}
				first := probes[0]
				if first["method"] != "POST" || first["path"] != path || first["query"] != "q='中文'&x=1" || first["body"] != "{\"body\":\"探针\"}" {
					t.Fatalf("Probe payload not preserved: %#v", first)
				}
				stamp, err := time.Parse(time.RFC3339, first["created_at"].(string))
				if err != nil || stamp.Before(started) || stamp.After(time.Now()) || stamp.Location() != time.UTC {
					t.Fatalf("created_at=%v parse=%v", first["created_at"], err)
				}
				if got := probeTxVersions(t, s); got != versions {
					t.Fatalf("successful Probe bumped versions\nbefore=%s\nafter=%s", versions, got)
				}
			}
		})
	}
}

func TestProbeTxStatementFailureRollsBackAndRetry(t *testing.T) {
	for _, fault := range []struct {
		name, trigger string
		seed          int
	}{
		{"insert", `CREATE TRIGGER fixture_probe_reject BEFORE INSERT ON api_probe BEGIN SELECT RAISE(ABORT,'fixture-probe-insert'); END`, 3},
		// The insert creates row 1003; trimming would delete original rows 1..3.
		// Reject the last deletion to exercise rollback of earlier deletions too.
		{"delete", `CREATE TRIGGER fixture_probe_reject BEFORE DELETE ON api_probe WHEN OLD.id=3 BEGIN SELECT RAISE(ABORT,'fixture-probe-delete'); END`, 1002},
	} {
		t.Run(fault.name, func(t *testing.T) {
			s := newProbeTxStore(t)
			seedProbeTxRows(t, s, fault.seed)
			before, versions := probeTxRows(t, s), probeTxVersions(t, s)
			if _, err := s.db.Exec(fault.trigger); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := s.Probe("POST", "/rejected", "q=1", "rejected-body"); err == nil || !strings.Contains(err.Error(), "fixture-probe-"+fault.name) {
					t.Fatalf("expected %s rejection: %v", fault.name, err)
				}
				assertProbeTxUnchanged(t, s, before, versions)
			}
			if _, err := s.db.Exec("DROP TRIGGER fixture_probe_reject"); err != nil {
				t.Fatal(err)
			}
			if err := s.Probe("POST", "/retry", "", "retry-body"); err != nil {
				t.Fatalf("retry after statement rejection: %v", err)
			}
			wantCount := fault.seed + 1
			if wantCount > 1000 {
				wantCount = 1000
			}
			if got := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM api_probe"); got != wantCount {
				t.Fatalf("retry count=%d want=%d", got, wantCount)
			}
			if got := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM api_probe WHERE path='/retry'"); got != 1 {
				t.Fatalf("retry row count=%d", got)
			}
			if got := probeTxVersions(t, s); got != versions {
				t.Fatal("retry advanced cache versions")
			}
		})
	}
}

func TestProbeTxDeferredCommitFailureAndPoolRecovery(t *testing.T) {
	s := newProbeTxStore(t)
	s.db.SetMaxOpenConns(1)
	seedProbeTxRows(t, s, 1002)
	before, versions := probeTxRows(t, s), probeTxVersions(t, s)
	if _, err := s.db.Exec(`CREATE TABLE fixture_probe_parent(id INTEGER PRIMARY KEY);
CREATE TABLE fixture_probe_child(parent_id INTEGER REFERENCES fixture_probe_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fixture_probe_commit AFTER INSERT ON api_probe
BEGIN INSERT INTO fixture_probe_child(parent_id) VALUES(999); END`); err != nil {
		t.Fatal(err)
	}

	// Prove both statements succeed and the trigger's actual FK violation is
	// deferred until COMMIT, rather than relying on an immediate-error mock.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_probe(method,path,query,body_preview,created_at) VALUES('GET','/fixture-proof','','','2000-01-01T00:00:00Z')`); err != nil {
			t.Fatalf("fixture insert failed before COMMIT: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM api_probe WHERE id NOT IN (SELECT id FROM api_probe ORDER BY id DESC LIMIT 1000)"); err != nil {
			t.Fatalf("fixture delete failed before COMMIT: %v", err)
		}
		var children int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture_probe_child").Scan(&children); err != nil || children != 1 {
			t.Fatalf("pending trigger child count=%d err=%v", children, err)
		}
		if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Fatalf("fixture COMMIT should fail: %v", err)
		}
		// Only the fixture proof is manually cleaned. Store.Probe below must
		// recover independently through its transaction owner.
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Fatalf("fixture proof cleanup: %v", err)
		}
	}()
	assertProbeTxUnchanged(t, s, before, versions)
	if err := s.Probe("POST", "/commit-rejected", "q=1", "bad"); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected real deferred Probe COMMIT failure: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A single-slot pool prevents a poisoned idle connection from hiding behind
	// another pooled connection. Recovery may safely discard/replace it; this
	// test deliberately makes no claim about physical connection identity.
	health, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("next transaction unavailable after rejected COMMIT: %v", err)
	}
	defer health.Rollback()
	if _, err := health.ExecContext(ctx, "INSERT INTO fixture_probe_parent(id) VALUES(1)"); err != nil {
		t.Fatalf("next transaction write: %v", err)
	}
	if err := health.Commit(); err != nil {
		t.Fatalf("next transaction COMMIT: %v", err)
	}
	assertProbeTxUnchanged(t, s, before, versions)
	if n := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM fixture_probe_child"); n != 0 {
		t.Fatalf("failed Probe retained trigger children: %d", n)
	}
	if n := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM fixture_probe_parent WHERE id=1"); n != 1 {
		t.Fatalf("next transaction was not durable: count=%d", n)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER fixture_probe_commit"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Probe("POST", "/recovered", "q=2", "good") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Probe after repair: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("Probe after repair exceeded recovery deadline: %v", ctx.Err())
	}
	if n := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM api_probe"); n != 1000 {
		t.Fatalf("recovered retention count=%d", n)
	}
	if n := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM api_probe WHERE path='/recovered' AND query='q=2' AND body_preview='good'"); n != 1 {
		t.Fatalf("recovered Probe not committed: count=%d", n)
	}
	if n := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM fixture_probe_child"); n != 0 {
		t.Fatalf("recovery unexpectedly retained failed trigger child: %d", n)
	}
	if got := probeTxVersions(t, s); got != versions {
		t.Fatal("recovered Probe advanced cache versions")
	}
}
