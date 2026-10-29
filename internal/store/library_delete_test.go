package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// indexSnapshot deliberately omits libraries. Capture every library column,
// including enabled/type/sort_order, rather than the enabled-only Library API.
func libraryDeleteRows(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertLibraryDeleteUnchanged(t *testing.T, f indexedDeleteFixture, snapshot, libraries string, versions [3]string) {
	t.Helper()
	if got := indexSnapshot(t, f.s); got != snapshot {
		t.Fatalf("failure changed index snapshot\nbefore=%s\nafter=%s", snapshot, got)
	}
	if got := libraryDeleteRows(t, f.s, "SELECT * FROM libraries ORDER BY id"); got != libraries {
		t.Fatalf("failure changed library rows\nbefore=%s\nafter=%s", libraries, got)
	}
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM libraries WHERE id=?", f.lib); n != 1 {
		t.Fatalf("failure lost target library: count=%d", n)
	}
	assertIndexedDeleteVersions(t, f, versions, false)
}

func TestDeleteIndexedLibrarySuccessAndMissing(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprintf("library_zero=%t", zero), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			if zero {
				if _, err := f.s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/fixture/zero'); UPDATE movies SET library_id=0 WHERE id=?", f.victim); err != nil {
					t.Fatal(err)
				}
				f.lib = 0
			}
			// A real lib:0 key must be retained, and must not be bumped when
			// deleting a nonzero library. It is never a sentinel domain.
			if err := f.s.BumpVersion(0); err != nil {
				t.Fatal(err)
			}
			zeroBefore := storedVersion(t, f.s, "lib:0:version")
			second, err := f.s.IndexMovie(Movie{LibraryID: f.lib, SourcePath: "/fixture/delete/second.strm", Title: "Second", Status: "success", Genres: []string{"Comedy"}}, 2, time.Unix(2, 0), []ActorRef{{Name: "Second actor"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.SavePlaybackForUser(1, second, 300, 2, "second-time", 11); err != nil {
				t.Fatal(err)
			}
			otherLibrary := libraryDeleteRows(t, f.s, "SELECT * FROM libraries WHERE id=?", f.other)
			otherRows := make(map[string]string)
			for _, table := range []string{"movies", "userdata", "movie_actors", "movie_features"} {
				key := "movie_id"
				if table == "movies" {
					key = "id"
				}
				query := "SELECT * FROM " + table + " WHERE " + key + "=? ORDER BY 1,2"
				otherRows[query] = libraryDeleteRows(t, f.s, query, f.survivor)
			}
			versions := indexedDeleteVersions(t, f)
			if err := f.s.DeleteIndexedLibrary(f.lib); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteCascades(t, f)
			secondFixture := f
			secondFixture.victim = second
			assertIndexedDeleteCascades(t, secondFixture)
			assertIndexedDeleteVersions(t, f, versions, true)
			if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM libraries WHERE id=?", f.lib); n != 0 {
				t.Fatalf("deleted library retained %d rows", n)
			}
			if got := libraryDeleteRows(t, f.s, "SELECT * FROM libraries WHERE id=?", f.other); got != otherLibrary {
				t.Fatalf("other library changed: before=%s after=%s", otherLibrary, got)
			}
			for query, before := range otherRows {
				if got := libraryDeleteRows(t, f.s, query, f.survivor); got != before {
					t.Fatalf("other-library rows changed for %s: before=%s after=%s", query, before, got)
				}
			}
			if !zero && storedVersion(t, f.s, "lib:0:version") != zeroBefore {
				t.Fatal("nonzero library deletion bumped lib:0")
			}
			if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM kv WHERE key=?", fmt.Sprintf("lib:%d:version", f.lib)); n != 1 {
				t.Fatalf("deleted library version key not retained: %d", n)
			}
			committed := indexSnapshot(t, f.s)
			libraries := libraryDeleteRows(t, f.s, "SELECT * FROM libraries ORDER BY id")
			versions = indexedDeleteVersions(t, f)
			for _, id := range []int64{f.lib, 987654321, -1} {
				if err := f.s.DeleteIndexedLibrary(id); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("missing library %d: %v", id, err)
				}
				if got := indexSnapshot(t, f.s); got != committed {
					t.Fatalf("missing delete changed snapshot: before=%s after=%s", committed, got)
				}
				if got := libraryDeleteRows(t, f.s, "SELECT * FROM libraries ORDER BY id"); got != libraries {
					t.Fatalf("missing delete changed libraries: before=%s after=%s", libraries, got)
				}
				assertIndexedDeleteVersions(t, f, versions, false)
			}
		})
	}
}

func TestDeleteIndexedLibraryEmpty(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	lib, err := f.s.AddLibrary("Empty", "/fixture/empty")
	if err != nil {
		t.Fatal(err)
	}
	f.lib = lib.ID
	// An empty library has never been indexed and has no version key yet.
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM kv WHERE key=?", fmt.Sprintf("lib:%d:version", f.lib)); n != 0 {
		t.Fatal("empty fixture unexpectedly has a version")
	}
	beforeGlobal := storedVersion(t, f.s, "g:version")
	unrelated := make(map[string]string)
	for _, table := range []string{"movies", "actors", "userdata", "movie_actors", "movie_features"} {
		query := "SELECT * FROM " + table + " ORDER BY 1,2"
		unrelated[query] = libraryDeleteRows(t, f.s, query)
	}
	if err := f.s.DeleteIndexedLibrary(f.lib); err != nil {
		t.Fatal(err)
	}
	assertIndexedDeleteVersions(t, f, [3]string{beforeGlobal, "0", storedVersion(t, f.s, fmt.Sprintf("lib:%d:version", f.other))}, true)
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM libraries WHERE id=?", f.lib); n != 0 {
		t.Fatalf("empty library still exists: %d", n)
	}
	for query, before := range unrelated {
		if got := libraryDeleteRows(t, f.s, query); got != before {
			t.Fatalf("empty delete changed unrelated rows for %s: before=%s after=%s", query, before, got)
		}
	}
	committed := indexSnapshot(t, f.s)
	if err := f.s.DeleteIndexedLibrary(f.lib); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty library repeat: %v", err)
	}
	if got := indexSnapshot(t, f.s); got != committed {
		t.Fatalf("repeat empty deletion changed snapshot: before=%s after=%s", committed, got)
	}
}

func TestDeleteIndexedLibraryTriggerRollback(t *testing.T) {
	for _, fault := range []struct{ name, clause string }{
		{"global_version", "BEFORE INSERT ON kv WHEN NEW.key='g:version'"},
		{"library_version", "BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version'"},
		{"movies", "BEFORE DELETE ON movies"},
		{"libraries", "BEFORE DELETE ON libraries"},
		{"userdata", "BEFORE DELETE ON userdata"},
		{"movie_actors", "BEFORE DELETE ON movie_actors"},
		{"movie_features", "BEFORE DELETE ON movie_features"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			// Include nondefault and disabled library fields in rollback checks.
			if _, err := f.s.db.Exec("UPDATE libraries SET name='Keep name',type='tvshows',enabled=0,sort_order=42 WHERE id=?", f.lib); err != nil {
				t.Fatal(err)
			}
			before := indexSnapshot(t, f.s)
			libraries := libraryDeleteRows(t, f.s, "SELECT * FROM libraries ORDER BY id")
			versions := indexedDeleteVersions(t, f)
			clause := fault.clause
			if fault.name == "library_version" {
				clause = fmt.Sprintf(clause, f.lib)
			}
			if _, err := f.s.db.Exec("CREATE TRIGGER fail_library_delete " + clause + " BEGIN SELECT RAISE(ABORT,'fixture-library-delete-failure'); END"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.DeleteIndexedLibrary(f.lib); err == nil || !strings.Contains(err.Error(), "fixture-library-delete-failure") {
				t.Fatalf("expected trigger failure: %v", err)
			}
			assertLibraryDeleteUnchanged(t, f, before, libraries, versions)
			if _, err := f.s.db.Exec("DROP TRIGGER fail_library_delete"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.DeleteIndexedLibrary(f.lib); err != nil {
				t.Fatalf("retry after trigger removal: %v", err)
			}
			assertIndexedDeleteCascades(t, f)
			assertIndexedDeleteVersions(t, f, versions, true)
			if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM libraries WHERE id=?", f.lib); n != 0 {
				t.Fatalf("retry retained target library: %d", n)
			}
		})
	}
}

func TestDeleteIndexedLibraryDeferredCommitFailure(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	f.s.db.SetMaxOpenConns(1)
	before := indexSnapshot(t, f.s)
	libraries := libraryDeleteRows(t, f.s, "SELECT * FROM libraries ORDER BY id")
	versions := indexedDeleteVersions(t, f)
	if _, err := f.s.db.Exec(`CREATE TABLE fixture_library_delete_parent(id INTEGER PRIMARY KEY);
CREATE TABLE fixture_library_delete_child(parent_id INTEGER REFERENCES fixture_library_delete_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_library_delete_commit AFTER DELETE ON libraries
BEGIN INSERT INTO fixture_library_delete_child(parent_id) VALUES(999); END`); err != nil {
		t.Fatal(err)
	}
	// Prove the actual DELETE statements succeed with this fixture: the
	// constraint is truly deferred, not an immediate statement failure.
	tx, err := f.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM movies WHERE library_id=?", f.lib); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM libraries WHERE id=?", f.lib); err != nil {
		t.Fatalf("fixture FK was not deferred: %v", err)
	}
	var pending int
	if err := tx.QueryRow("SELECT COUNT(*) FROM fixture_library_delete_child").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("deferred trigger did not insert its pending violation: count=%d err=%v", pending, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteIndexedLibrary(f.lib); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected deferred COMMIT failure: %v", err)
	}
	// Bound the next write on the sole connection to expose a pool leak or a
	// physical SQLite transaction left open after database/sql marks Tx done.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	health, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("pool could not start transaction after failed COMMIT: %v", err)
	}
	defer health.Rollback()
	if _, err := health.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('fixture:library-pool-health','ok')"); err != nil {
		t.Fatalf("pool write after failed COMMIT: %v", err)
	}
	if err := health.Commit(); err != nil {
		t.Fatalf("pool commit after failed COMMIT: %v", err)
	}
	assertLibraryDeleteUnchanged(t, f, before, libraries, versions)
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM fixture_library_delete_child"); n != 0 {
		t.Fatalf("deferred trigger rows survived rollback: %d", n)
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fail_library_delete_commit"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteIndexedLibrary(f.lib); err != nil {
		t.Fatalf("retry after failed COMMIT: %v", err)
	}
	assertIndexedDeleteCascades(t, f)
	assertIndexedDeleteVersions(t, f, versions, true)
	if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM libraries WHERE id=?", f.lib); n != 0 {
		t.Fatalf("retry retained target library: %d", n)
	}
}

// P2-10：DeleteLibrary 现在和 DeleteIndexedLibrary 走同一条版本化路径。原来它用
// 裸 s.db.Begin 删行：绕过 writeMu、手写 rollback、而且删完不发布版本，缓存与
// 客户端看不到"库被删了"。语义修正（本项要求）：成功删除必须 bump 全局版本与
// 该库版本；库不存在时行为不变，仍返回 sql.ErrNoRows 且整体回滚、不发布版本。
func TestDeleteLibraryBumpsVersionsAndRejectsMissing(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	versions := indexedDeleteVersions(t, f)
	if err := f.s.DeleteLibrary(f.lib); err != nil {
		t.Fatal(err)
	}
	assertIndexedDeleteCascades(t, f)
	assertIndexedDeleteVersions(t, f, versions, true)
	committed := indexedDeleteVersions(t, f)
	snapshot := indexSnapshot(t, f.s)
	if err := f.s.DeleteLibrary(f.lib); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a missing library: err=%v want sql.ErrNoRows", err)
	}
	if got := indexSnapshot(t, f.s); got != snapshot {
		t.Fatalf("failed delete changed the snapshot\nbefore=%s\nafter=%s", snapshot, got)
	}
	if got := indexedDeleteVersions(t, f); got != committed {
		t.Fatalf("failed delete published versions: %v want %v", got, committed)
	}
	if got := f.s.Version("g:version"); got != committed[0] {
		t.Fatalf("published global version=%s want %s", got, committed[0])
	}
}
