package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const manualIndexSource = "/fixture/delete/victim.strm"
const manualIndexNFO = "/fixture/delete/edited.nfo"

// Keep every movie column (including future additions), actor, relation, feature,
// and playback value in the shared snapshot except the three allowed target
// fields. Version domains are asserted separately, including their local mirror.
func manualIndexProtectedSnapshot(t *testing.T, f indexedDeleteFixture) string {
	t.Helper()
	rows, err := f.s.db.Query("SELECT * FROM movies LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot []any
	if err := json.Unmarshal([]byte(indexSnapshot(t, f.s)), &snapshot); err != nil {
		t.Fatal(err)
	}
	var protected []any
	for _, entry := range snapshot[:len(snapshot)-1] {
		row := entry.([]any)
		if len(row) == 2 {
			if key, ok := row[0].(string); ok && (key == "g:version" || strings.HasPrefix(key, "lib:")) {
				continue
			}
		}
		if len(row) == len(columns) && row[0] == float64(f.victim) {
			for i, column := range columns {
				if column == "status" || column == "nfo_path" || column == "updated_at" {
					row[i] = nil
				}
			}
		}
		protected = append(protected, row)
	}
	body, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertManualIndexSaved(t *testing.T, f indexedDeleteFixture, earliest time.Time) {
	t.Helper()
	var status, path, updated string
	if err := f.s.db.QueryRow("SELECT status,nfo_path,updated_at FROM movies WHERE id=?", f.victim).Scan(&status, &path, &updated); err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse(time.RFC3339, updated)
	if status != "manual" || path != manualIndexNFO || err != nil || stamp.Before(earliest.Truncate(time.Second)) || stamp.After(time.Now()) {
		t.Fatalf("saved fields: status=%q path=%q updated=%q parse=%v earliest=%v", status, path, updated, err, earliest)
	}
}

func TestMarkIndexedManualSuccessPreservesIndexAndRepeatedPath(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	// Avoid subsecond timestamp coincidences masking a missing timestamp update.
	if _, err := f.s.db.Exec("UPDATE movies SET updated_at='2000-01-01T00:00:00Z',nfo_path='old.nfo' WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	protected := manualIndexProtectedSnapshot(t, f)
	for attempt := 0; attempt < 2; attempt++ {
		before := indexedDeleteVersions(t, f)
		started := time.Now()
		if err := f.s.MarkIndexedManual(manualIndexSource, manualIndexNFO); err != nil {
			t.Fatal(err)
		}
		assertManualIndexSaved(t, f, started)
		assertIndexedDeleteVersions(t, f, before, true)
		if after := manualIndexProtectedSnapshot(t, f); after != protected {
			t.Fatalf("attempt %d changed protected index fields\nbefore=%s\nafter=%s", attempt, protected, after)
		}
	}
}

func TestMarkIndexedManualActualLibraryVersionDomains(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprintf("actual_zero_%t", zero), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			if zero {
				if _, err := f.s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/fixture/zero'); UPDATE movies SET library_id=0 WHERE id=?", f.victim); err != nil {
					t.Fatal(err)
				}
				f.lib = 0
			}
			if err := f.s.BumpVersion(0); err != nil {
				t.Fatal(err)
			}
			before := indexedDeleteVersions(t, f)
			zeroBefore := storedVersion(t, f.s, "lib:0:version")
			if err := f.s.MarkIndexedManual(manualIndexSource, manualIndexNFO); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteVersions(t, f, before, true)
			if !zero && storedVersion(t, f.s, "lib:0:version") != zeroBefore {
				t.Fatal("nonzero actual library incorrectly advanced lib:0")
			}
		})
	}
}

func TestMarkIndexedManualNullableLibraryOnlyGlobal(t *testing.T) {
	// The current migrated schema forbids NULL library_id. Exercise the defensive
	// nullable RETURNING branch using a separate minimal compatibility fixture,
	// never by weakening or rewriting the production schema/migrations.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "nullable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Store{db: db}
	if _, err := db.Exec(`CREATE TABLE kv(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE movies(source_path TEXT UNIQUE,library_id INTEGER,status TEXT,nfo_path TEXT,updated_at TEXT);
INSERT INTO movies VALUES('/nullable.strm',NULL,'success','old','old');
INSERT INTO kv VALUES('lib:0:version','17'),('lib:9:version','23');`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIndexedManual("/nullable.strm", manualIndexNFO); err != nil {
		t.Fatal(err)
	}
	if got := storedVersion(t, s, "g:version"); got != "1" || s.Version("g:version") != got {
		t.Fatalf("global=%s mirror=%s", got, s.Version("g:version"))
	}
	if storedVersion(t, s, "lib:0:version") != "17" || storedVersion(t, s, "lib:9:version") != "23" || indexedDeleteCount(t, s, "SELECT COUNT(*) FROM kv") != 3 {
		t.Fatal("NULL library changed a library domain")
	}
	var library sql.NullInt64
	var status, path string
	if err := db.QueryRow("SELECT library_id,status,nfo_path FROM movies").Scan(&library, &status, &path); err != nil || library.Valid || status != "manual" || path != manualIndexNFO {
		t.Fatalf("nullable row: library=%v status=%q path=%q err=%v", library, status, path, err)
	}
}

func TestMarkIndexedManualUnknownSourceRollsBackVersions(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	before := indexSnapshot(t, f.s)
	for attempt := 0; attempt < 2; attempt++ {
		if err := f.s.MarkIndexedManual("/fixture/missing.strm", manualIndexNFO); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing source: %v", err)
		}
		if after := indexSnapshot(t, f.s); after != before {
			t.Fatalf("missing source changed snapshot\nbefore=%s\nafter=%s", before, after)
		}
	}
}

func TestMarkIndexedManualTriggerRollbackAndRetry(t *testing.T) {
	for _, fault := range []struct{ name, clause string }{
		{"global", "BEFORE INSERT ON kv WHEN NEW.key='g:version'"},
		{"library", "BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version'"},
		{"update", "BEFORE UPDATE ON movies"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			before := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			protected := manualIndexProtectedSnapshot(t, f)
			clause := fault.clause
			if fault.name == "library" {
				clause = fmt.Sprintf(clause, f.lib)
			}
			if _, err := f.s.db.Exec("CREATE TRIGGER fail_manual " + clause + " BEGIN SELECT RAISE(ABORT,'fixture-manual-failure'); END"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.MarkIndexedManual(manualIndexSource, manualIndexNFO); err == nil || !strings.Contains(err.Error(), "fixture-manual-failure") {
				t.Fatalf("expected %s failure: %v", fault.name, err)
			}
			if after := indexSnapshot(t, f.s); after != before {
				t.Fatalf("failure changed full snapshot\nbefore=%s\nafter=%s", before, after)
			}
			if _, err := f.s.db.Exec("DROP TRIGGER fail_manual"); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if err := f.s.MarkIndexedManual(manualIndexSource, manualIndexNFO); err != nil {
				t.Fatalf("retry: %v", err)
			}
			assertManualIndexSaved(t, f, started)
			assertIndexedDeleteVersions(t, f, versions, true)
			if after := manualIndexProtectedSnapshot(t, f); after != protected {
				t.Fatal("retry changed protected index")
			}
		})
	}
}

func TestMarkIndexedManualDeferredCommitFailureAndPoolRecovery(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	f.s.db.SetMaxOpenConns(1)
	before := indexSnapshot(t, f.s)
	versions := indexedDeleteVersions(t, f)
	protected := manualIndexProtectedSnapshot(t, f)
	if _, err := f.s.db.Exec(`CREATE TABLE fixture_manual_parent(id INTEGER PRIMARY KEY);
CREATE TABLE fixture_manual_child(parent_id INTEGER REFERENCES fixture_manual_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_manual_commit AFTER UPDATE ON movies
BEGIN INSERT INTO fixture_manual_child(parent_id) VALUES(999); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MarkIndexedManual(manualIndexSource, manualIndexNFO); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected real deferred COMMIT rejection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("sole connection unavailable after COMMIT failure: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('fixture:manual-pool-health','ok')"); err != nil {
		t.Fatalf("next transaction write: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("next transaction COMMIT: %v", err)
	}
	if after := indexSnapshot(t, f.s); after != before {
		t.Fatalf("COMMIT failure changed full snapshot\nbefore=%s\nafter=%s", before, after)
	}
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM fixture_manual_child"); n != 0 {
		t.Fatalf("deferred fixture writes survived rollback: %d", n)
	}
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM fixture_manual_parent"); n != 0 {
		t.Fatalf("deferred fixture parent changed: %d", n)
	}
	if storedVersion(t, f.s, "fixture:manual-pool-health") != "ok" {
		t.Fatal("subsequent transaction was not committed")
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fail_manual_commit"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := f.s.MarkIndexedManual(manualIndexSource, manualIndexNFO); err != nil {
		t.Fatalf("retry after COMMIT failure: %v", err)
	}
	assertManualIndexSaved(t, f, started)
	assertIndexedDeleteVersions(t, f, versions, true)
	if after := manualIndexProtectedSnapshot(t, f); after != protected {
		t.Fatal("retry changed protected index")
	}
}

func TestMarkIndexedManualLegacyMarkManualRemainsUnversioned(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	versions := indexedDeleteVersions(t, f)
	protected := manualIndexProtectedSnapshot(t, f)
	started := time.Now()
	if err := f.s.MarkManual(manualIndexSource, manualIndexNFO); err != nil {
		t.Fatal(err)
	}
	assertManualIndexSaved(t, f, started)
	assertIndexedDeleteVersions(t, f, versions, false)
	if after := manualIndexProtectedSnapshot(t, f); after != protected {
		t.Fatal("legacy mark changed protected index")
	}
}
