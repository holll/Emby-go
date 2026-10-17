package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every fixture owns a temporary database. The victim is in the second library
// so invalidation cannot accidentally use a default library ID.
type indexedDeleteFixture struct {
	s                            *Store
	lib, other, victim, survivor int64
}

func newIndexedDeleteFixture(t *testing.T) indexedDeleteFixture {
	t.Helper()
	s, other := newFeatureStore(t)
	s.waitFeaturesReady()
	lib, err := s.AddLibrary("Delete target", "/fixture/delete")
	if err != nil {
		t.Fatal(err)
	}
	f := indexedDeleteFixture{s: s, lib: lib.ID, other: other}
	for _, item := range []struct {
		lib         int64
		path, actor string
		id          *int64
	}{
		{f.lib, "/fixture/delete/victim.strm", "Victim actor", &f.victim},
		{f.other, "/fixture/other/survivor.strm", "Other actor", &f.survivor},
	} {
		id, err := s.IndexMovie(Movie{LibraryID: item.lib, SourcePath: item.path, Title: item.actor, Status: "success", Genres: []string{"Drama"}}, 1, time.Unix(1, 0), []ActorRef{{Name: item.actor, AvatarURL: "fixture-avatar"}})
		if err != nil {
			t.Fatal(err)
		}
		*item.id = id
		if err := s.SavePlayback(id, 120, 3, "fixture-time", 99); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func indexedDeleteCount(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func indexedDeleteVersions(t *testing.T, f indexedDeleteFixture) [3]string {
	t.Helper()
	return [3]string{storedVersion(t, f.s, "g:version"), storedVersion(t, f.s, fmt.Sprintf("lib:%d:version", f.lib)), storedVersion(t, f.s, fmt.Sprintf("lib:%d:version", f.other))}
}

func assertIndexedDeleteVersions(t *testing.T, f indexedDeleteFixture, before [3]string, bump bool) {
	t.Helper()
	want := before
	if bump {
		for i := 0; i < 2; i++ {
			n, err := strconv.Atoi(before[i])
			if err != nil {
				t.Fatal(err)
			}
			want[i] = strconv.Itoa(n + 1)
		}
	}
	if got := indexedDeleteVersions(t, f); got != want {
		t.Fatalf("versions=%v want=%v", got, want)
	}
	if got := f.s.Version("g:version"); got != want[0] {
		t.Fatalf("published global=%s want=%s", got, want[0])
	}
}

func assertIndexedDeleteCascades(t *testing.T, f indexedDeleteFixture) {
	t.Helper()
	for _, table := range []string{"movies", "userdata", "movie_actors", "movie_features"} {
		key := "movie_id"
		if table == "movies" {
			key = "id"
		}
		if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM "+table+" WHERE "+key+"=?", f.victim); n != 0 {
			t.Errorf("%s retained %d victim rows", table, n)
		}
		if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM "+table+" WHERE "+key+"=?", f.survivor); n == 0 {
			t.Errorf("%s lost other-library rows", table)
		}
	}
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM actors WHERE name='Victim actor' AND avatar_url='fixture-avatar'"); n != 1 {
		t.Fatalf("actor dictionary changed: %d", n)
	}
}

func TestDeleteIndexedMovieSuccessAndMissing(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	before := indexedDeleteVersions(t, f)
	if err := f.s.DeleteIndexedMovie(f.victim); err != nil {
		t.Fatal(err)
	}
	assertIndexedDeleteCascades(t, f)
	assertIndexedDeleteVersions(t, f, before, true)
	committed := indexSnapshot(t, f.s)
	for _, id := range []int64{f.victim, f.victim + f.survivor + 1000} {
		if err := f.s.DeleteIndexedMovie(id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing id=%d err=%v", id, err)
		}
		if got := indexSnapshot(t, f.s); got != committed {
			t.Fatalf("missing delete changed snapshot\nbefore=%s\nafter=%s", committed, got)
		}
	}
}

func TestDeleteIndexedAndReconcileTriggerRollback(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		for _, fault := range []struct{ name, clause string }{
			{"global", "BEFORE INSERT ON kv WHEN NEW.key='g:version'"},
			{"library", "BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version'"},
			{"delete", "BEFORE DELETE ON movies"},
			{"userdata", "BEFORE DELETE ON userdata"},
			{"actors", "BEFORE DELETE ON movie_actors"},
			{"features", "BEFORE DELETE ON movie_features"},
		} {
			t.Run(fmt.Sprintf("reconcile=%t/%s", reconcile, fault.name), func(t *testing.T) {
				f := newIndexedDeleteFixture(t)
				before := indexSnapshot(t, f.s)
				versions := indexedDeleteVersions(t, f)
				clause := fault.clause
				if fault.name == "library" {
					clause = fmt.Sprintf(clause, f.lib)
				}
				if _, err := f.s.db.Exec("CREATE TRIGGER fail_delete " + clause + " BEGIN SELECT RAISE(ABORT,'fixture-delete-failure'); END"); err != nil {
					t.Fatal(err)
				}
				run := func() error {
					if reconcile {
						return f.s.ReconcileMissingSources(f.lib, nil)
					}
					return f.s.DeleteIndexedMovie(f.victim)
				}
				if err := run(); err == nil || !strings.Contains(err.Error(), "fixture-delete-failure") {
					t.Fatalf("expected injected failure: %v", err)
				}
				if after := indexSnapshot(t, f.s); after != before {
					t.Fatalf("failure changed full snapshot\nbefore=%s\nafter=%s", before, after)
				}
				if _, err := f.s.db.Exec("DROP TRIGGER fail_delete"); err != nil {
					t.Fatal(err)
				}
				if err := run(); err != nil {
					t.Fatal(err)
				}
				assertIndexedDeleteCascades(t, f)
				assertIndexedDeleteVersions(t, f, versions, true)
			})
		}
	}
}

func TestDeleteIndexedAndReconcileDeferredCommitFailure(t *testing.T) {
	for _, mode := range []string{"indexed", "reconcile", "legacy-missing"} {
		t.Run(mode, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			f.s.db.SetMaxOpenConns(1)
			before := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			// DELETE succeeds; only COMMIT rejects this deferred foreign key.
			if _, err := f.s.db.Exec(`CREATE TABLE fixture_delete_parent(id INTEGER PRIMARY KEY);
CREATE TABLE fixture_delete_child(parent_id INTEGER REFERENCES fixture_delete_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_delete_commit AFTER DELETE ON movies
BEGIN INSERT INTO fixture_delete_child(parent_id) VALUES(999); END`); err != nil {
				t.Fatal(err)
			}
			run := func() error {
				switch mode {
				case "indexed":
					return f.s.DeleteIndexedMovie(f.victim)
				case "reconcile":
					return f.s.ReconcileMissingSources(f.lib, nil)
				default:
					return f.s.DeleteMissingSources(f.lib, nil)
				}
			}
			if err := run(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				t.Fatalf("expected deferred commit failure: %v", err)
			}
			// Bound the probe so a leaked sole connection fails promptly.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("pool could not start next transaction: %v", err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('fixture:pool-health','ok')"); err != nil {
				t.Fatalf("pool write after failed commit: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("pool commit after failed commit: %v", err)
			}
			if after := indexSnapshot(t, f.s); after != before {
				t.Fatalf("commit failure changed full snapshot\nbefore=%s\nafter=%s", before, after)
			}
			if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM fixture_delete_child"); n != 0 {
				t.Fatalf("deferred fixture writes survived rollback: %d", n)
			}
			if _, err := f.s.db.Exec("DROP TRIGGER fail_delete_commit"); err != nil {
				t.Fatal(err)
			}
			if err := run(); err != nil {
				t.Fatalf("retry after commit failure: %v", err)
			}
			assertIndexedDeleteCascades(t, f)
			assertIndexedDeleteVersions(t, f, versions, mode != "legacy-missing")
		})
	}
}

func TestReconcileMissingSourcesChunkRollbackAndCompleteInventory(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	// Bulk insertion is unversioned so only the tested operation bumps versions.
	// Together with victim there are 502 missing paths and one inventory keeper.
	tx, err := f.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 502; i++ {
		path := fmt.Sprintf("/fixture/delete/bulk-%04d.strm", i)
		if i == 501 {
			path = "/fixture/delete/keep.strm"
		}
		if _, err := tx.Exec("INSERT INTO movies(library_id,source_path,status,created_at,updated_at) VALUES(?,?,'success','fixture','fixture')", f.lib, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := indexSnapshot(t, f.s)
	versions := indexedDeleteVersions(t, f)
	// Count deletions instead of relying on SELECT's unspecified path order.
	// The 501st deletion is in chunk two; all of chunk one must roll back.
	if _, err := f.s.db.Exec(`CREATE TABLE fixture_delete_progress(n INTEGER NOT NULL);
INSERT INTO fixture_delete_progress(n) VALUES(0);
CREATE TRIGGER fail_second_delete_chunk BEFORE DELETE ON movies
BEGIN
 UPDATE fixture_delete_progress SET n=n+1;
 SELECT CASE WHEN (SELECT n FROM fixture_delete_progress)>500 THEN RAISE(ABORT,'fixture-second-chunk') END;
END`); err != nil {
		t.Fatal(err)
	}
	inventory := map[string]struct{}{"/fixture/delete/keep.strm": {}}
	if err := f.s.ReconcileMissingSources(f.lib, inventory); err == nil || !strings.Contains(err.Error(), "fixture-second-chunk") {
		t.Fatalf("expected second chunk failure: %v", err)
	}
	if after := indexSnapshot(t, f.s); after != before {
		t.Fatalf("second chunk failure changed full snapshot\nbefore=%s\nafter=%s", before, after)
	}
	if n := indexedDeleteCount(t, f.s, "SELECT n FROM fixture_delete_progress"); n != 0 {
		t.Fatalf("first chunk progress survived rollback: %d", n)
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fail_second_delete_chunk"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReconcileMissingSources(f.lib, inventory); err != nil {
		t.Fatal(err)
	}
	assertIndexedDeleteCascades(t, f)
	assertIndexedDeleteVersions(t, f, versions, true)
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM movies WHERE library_id=?", f.lib); n != 1 {
		t.Fatalf("retry left %d target movies, want keeper only", n)
	}
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM movies WHERE library_id=? AND source_path=?", f.lib, "/fixture/delete/keep.strm"); n != 1 {
		t.Fatal("complete inventory keeper was deleted")
	}
	// No missing records still publishes scan completion.
	versions = indexedDeleteVersions(t, f)
	if err := f.s.ReconcileMissingSources(f.lib, inventory); err != nil {
		t.Fatal(err)
	}
	assertIndexedDeleteVersions(t, f, versions, true)
	// Explicitly empty and nil complete inventories both mean delete all.
	for _, empty := range []map[string]struct{}{{}, nil} {
		versions = indexedDeleteVersions(t, f)
		if err := f.s.ReconcileMissingSources(f.lib, empty); err != nil {
			t.Fatal(err)
		}
		assertIndexedDeleteVersions(t, f, versions, true)
		if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM movies WHERE library_id=?", f.lib); n != 0 {
			t.Fatalf("empty complete inventory retained %d target movies", n)
		}
		assertIndexedDeleteCascades(t, f)
	}
}
