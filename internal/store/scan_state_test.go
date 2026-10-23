package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func scanFixture(t *testing.T) (*Store, Library) {
	t.Helper()
	s, id := newFeatureStore(t)
	s.waitFeaturesReady()
	lib, err := s.Library(id)
	if err != nil {
		t.Fatal(err)
	}
	return s, lib
}
func scanMovie(lib Library, path string) ScanGroupMovie {
	return ScanGroupMovie{Movie: Movie{LibraryID: lib.ID, SourcePath: path, Title: "Title", Status: "success", Genres: []string{"Genre"}}, Size: 42, Mtime: time.Unix(1, 0), Actors: []ActorRef{{Name: "Actor"}}}
}
func commitScan(t *testing.T, s *Store, lib Library, key string, movies []ScanGroupMovie, sources []ScanGroupSource) ScanGroupCommitResult {
	t.Helper()
	r, err := s.CommitScanGroupChecked(lib, key, "fingerprint", 1, "success", movies, sources)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func scanSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	return indexSnapshot(t, s) + libraryDeleteRows(t, s, "SELECT * FROM scan_groups ORDER BY library_id,group_key") + libraryDeleteRows(t, s, "SELECT * FROM scan_group_sources ORDER BY library_id,group_key,source_path")
}

func TestScanGroupRollbackEveryStage(t *testing.T) {
	faults := []struct{ name, trigger string }{
		{"global", `BEFORE INSERT ON kv WHEN NEW.key='g:version'`},
		{"library", `BEFORE INSERT ON kv WHEN NEW.key LIKE 'lib:%:version'`},
		{"movie", `BEFORE INSERT ON movies`},
		{"actor", `BEFORE INSERT ON movie_actors`},
		{"feature", `BEFORE INSERT ON movie_features`},
		{"delete", `BEFORE DELETE ON movies`},
		{"state", `BEFORE INSERT ON scan_groups`},
		{"source", `BEFORE INSERT ON scan_group_sources`},
		{"commit", ""},
	}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			s, lib := scanFixture(t)
			a, b := scanMovie(lib, "/a.strm"), scanMovie(lib, "/b.strm")
			first := commitScan(t, s, lib, "g", []ScanGroupMovie{a, b}, []ScanGroupSource{{Path: a.Movie.SourcePath}, {Path: b.Movie.SourcePath}})
			if err := s.SavePlaybackForUser(1, first.MovieIDs[a.Movie.SourcePath], 12, 2, "date", 9); err != nil {
				t.Fatal(err)
			}
			before := scanSnapshot(t, s)
			if f.name == "commit" {
				_, err := s.db.Exec(`CREATE TABLE scan_parent(id INTEGER PRIMARY KEY); CREATE TABLE scan_child(id INTEGER REFERENCES scan_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER scan_fault AFTER INSERT ON scan_groups BEGIN INSERT INTO scan_child VALUES(123); END`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.Exec("CREATE TRIGGER scan_fault " + f.trigger + " BEGIN SELECT RAISE(ABORT,'scan-fault'); END"); err != nil {
					t.Fatal(err)
				}
			}
			a.Movie.Title = "Changed"
			a.Movie.Genres = []string{"ChangedGenre"}
			r, err := s.CommitScanGroupChecked(lib, "g", "next", 1, "success", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath}, {Path: b.Movie.SourcePath}})
			if err == nil || len(r.MovieIDs) != 0 || r.Added+r.Updated+r.Deleted != 0 {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if got := scanSnapshot(t, s); got != before {
				t.Fatalf("rollback changed snapshot\nbefore=%s\nafter=%s", before, got)
			}
			if _, err := s.db.Exec("DROP TRIGGER scan_fault"); err != nil {
				t.Fatal(err)
			}
			next := commitScan(t, s, lib, "g", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath}, {Path: b.Movie.SourcePath}})
			if next.Updated != 1 || next.Deleted != 1 || next.MovieIDs[a.Movie.SourcePath] != first.MovieIDs[a.Movie.SourcePath] {
				t.Fatalf("recovery=%+v", next)
			}
		})
	}
}

func TestScanGroupManualIdentityAndMerge(t *testing.T) {
	s, lib := scanFixture(t)
	part := scanMovie(lib, "/CD2.strm")
	part.Movie.Status = "manual"
	original := commitScan(t, s, lib, "cd2", []ScanGroupMovie{part}, []ScanGroupSource{{Path: part.Movie.SourcePath, Role: "standalone"}})
	id := original.MovieIDs[part.Movie.SourcePath]
	if err := s.SavePlaybackForUser(1, id, 12, 3, "date", 9); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := s.db.QueryRow("SELECT created_at FROM movies WHERE id=?", id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	// Missing NFO may build a pending record but must not revoke manual status.
	beforeManual := indexSnapshot(t, s)
	part.Movie.Status = "pending"
	part.Movie.Title = ""
	part.Movie.Genres = nil
	part.Actors = nil
	retained := commitScan(t, s, lib, "cd2", []ScanGroupMovie{part}, []ScanGroupSource{{Path: part.Movie.SourcePath, Role: "standalone"}})
	if indexSnapshot(t, s) != beforeManual {
		t.Fatal("pending input erased manual metadata, actors, features, userdata or version")
	}
	var status, afterCreated string
	if err := s.db.QueryRow("SELECT status,created_at FROM movies WHERE id=?", id).Scan(&status, &afterCreated); err != nil {
		t.Fatal(err)
	}
	if retained.MovieIDs[part.Movie.SourcePath] != id || status != "manual" || created != afterCreated {
		t.Fatalf("identity/status changed: %+v %s", retained, status)
	}
	primary := scanMovie(lib, "/CD1.strm")
	primary.Movie.AdditionalParts = []string{part.Movie.SourcePath}
	merged := commitScan(t, s, lib, "merged", []ScanGroupMovie{primary}, []ScanGroupSource{{Path: primary.Movie.SourcePath, Role: "primary"}, {Path: part.Movie.SourcePath, Role: "part"}})
	if merged.MovieIDs[part.Movie.SourcePath] != id || merged.Deleted != 0 {
		t.Fatalf("manual part lost: %+v", merged)
	}
	groups, err := s.LoadScanGroups(lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups["merged"].GroupKey != "merged" {
		t.Fatalf("overlapping baseline survived: %+v", groups)
	}
	for _, source := range groups["merged"].Sources {
		if source.Path == part.Movie.SourcePath && source.ExpectedMovieID != id {
			t.Fatalf("manual evidence lost: %+v", source)
		}
	}
	cleanup, err := s.ReconcileScanInventoryChecked(lib, merged.MovieIDs, []string{"merged"})
	if err != nil || cleanup.DeletedMovies != 0 {
		t.Fatalf("cleanup=%+v %v", cleanup, err)
	}
	var plays int
	if err := s.db.QueryRow("SELECT play_count FROM userdata WHERE user_id=1 AND movie_id=?", id).Scan(&plays); err != nil || plays != 3 {
		t.Fatalf("userdata=%d %v", plays, err)
	}
	// A complete inventory that proves the manual source absent may delete it.
	_, err = s.ReconcileScanInventoryChecked(lib, map[string]int64{primary.Movie.SourcePath: merged.MovieIDs[primary.Movie.SourcePath]}, []string{"merged"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT status FROM movies WHERE id=?", id).Scan(&status); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing manual source retained: %v", err)
	}
}

func TestScanGroupLegacyRowsAndSkippedEvidence(t *testing.T) {
	s, lib := scanFixture(t)
	a, b := scanMovie(lib, "/CD1.strm"), scanMovie(lib, "/CD2.strm")
	if _, err := s.UpsertMovie(b.Movie, b.Size, b.Mtime); err != nil {
		t.Fatal(err)
	}
	a.Movie.AdditionalParts = []string{b.Movie.SourcePath}
	r := commitScan(t, s, lib, "merged", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath, Role: "primary"}, {Path: b.Movie.SourcePath, Role: "part"}})
	if r.Added != 1 || r.Deleted != 1 {
		t.Fatalf("legacy obsolete part=%+v", r)
	}
	// Invalid sources intentionally produce no movie, but are still dependencies.
	skipped := commitScan(t, s, lib, "merged", nil, []ScanGroupSource{{Path: a.Movie.SourcePath, Role: "primary"}, {Path: b.Movie.SourcePath, Role: "part"}})
	if skipped.Deleted != 1 || len(skipped.MovieIDs) != 0 {
		t.Fatalf("skipped=%+v", skipped)
	}
	groups, err := s.LoadScanGroups(lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups["merged"].Sources) != 2 {
		t.Fatalf("dependencies lost: %+v", groups)
	}
}

func TestScanCheckedLibraryAndInventoryCAS(t *testing.T) {
	s, lib := scanFixture(t)
	a := scanMovie(lib, "/a.strm")
	r := commitScan(t, s, lib, "g", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath}})
	before := scanSnapshot(t, s)
	for _, expected := range []map[string]int64{nil, {a.Movie.SourcePath: r.MovieIDs[a.Movie.SourcePath] + 1}, {"/missing": 99}, {a.Movie.SourcePath: 0}} {
		if _, err := s.ReconcileScanInventoryChecked(lib, expected, nil); !errors.Is(err, ErrScanInventoryChanged) {
			t.Fatalf("expected=%v err=%v", expected, err)
		}
		if scanSnapshot(t, s) != before {
			t.Fatal("stale evidence mutated index/state")
		}
	}
	if _, err := s.db.Exec("UPDATE libraries SET path=? WHERE id=?", lib.Path+"/moved", lib.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitScanGroupChecked(lib, "g", "next", 1, "success", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath}}); !errors.Is(err, ErrScanLibraryChanged) {
		t.Fatalf("commit=%v", err)
	}
	if _, err := s.ReconcileScanInventoryChecked(lib, r.MovieIDs, []string{"g"}); !errors.Is(err, ErrScanLibraryChanged) {
		t.Fatalf("reconcile=%v", err)
	}
	if scanSnapshot(t, s) != before {
		t.Fatal("relocated library changed index/state")
	}
	for _, invalid := range [][]ScanGroupMovie{{scanMovie(Library{ID: lib.ID + 100}, "/a.strm")}, {a, a}} {
		if _, err := s.CommitScanGroupChecked(lib, "g", "next", 1, "success", invalid, []ScanGroupSource{{Path: a.Movie.SourcePath}}); !errors.Is(err, ErrScanInventoryChanged) {
			t.Fatalf("invalid=%v", err)
		}
	}
}

func TestScanUnchangedReconcileNoActualDML(t *testing.T) {
	s, lib := scanFixture(t)
	a := scanMovie(lib, "/a.strm")
	r := commitScan(t, s, lib, "g", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath}})
	before := scanSnapshot(t, s)
	for _, table := range []string{"libraries", "movies", "actors", "movie_actors", "movie_features", "kv", "scan_groups", "scan_group_sources"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			q := fmt.Sprintf("CREATE TRIGGER no_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'unexpected-dml'); END", table, op, op, table)
			if _, err := s.db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := s.ReconcileScanInventoryChecked(lib, r.MovieIDs, []string{"g"})
	if err != nil || result != (ScanReconcileResult{}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if scanSnapshot(t, s) != before {
		t.Fatal("zero-change pass changed snapshot")
	}
}

func TestScanBaselineInvalidationEntrypoints(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Store, Library, ScanGroupMovie, int64) error
	}{
		{"UpsertMovie", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			_, e := s.UpsertMovie(m.Movie, m.Size, m.Mtime)
			return e
		}},
		{"IndexMovie", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			_, e := s.IndexMovie(m.Movie, m.Size, m.Mtime, m.Actors)
			return e
		}},
		{"ReplaceActors", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.ReplaceActors(id, []ActorRef{{Name: "Changed"}})
		}},
		{"SetActorAvatar", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.SetActorAvatar("Actor", "avatar", "")
		}},
		{"SetIndexedActorAvatar", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.SetIndexedActorAvatar("Actor", "avatar", "")
		}},
		{"MarkManual", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.MarkManual(m.Movie.SourcePath, "file.nfo")
		}},
		{"SetIndexedImage", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.SetIndexedImage(id, "poster", "new.jpg")
		}},
		{"MarkIndexedManual", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.MarkIndexedManual(m.Movie.SourcePath, "new.nfo")
		}},
		{"DeleteIndexedMovie", func(s *Store, l Library, m ScanGroupMovie, id int64) error { return s.DeleteIndexedMovie(id) }},
		{"DeleteLibrary", func(s *Store, l Library, m ScanGroupMovie, id int64) error { return s.DeleteLibrary(l.ID) }},
		{"DeleteIndexedLibrary", func(s *Store, l Library, m ScanGroupMovie, id int64) error { return s.DeleteIndexedLibrary(l.ID) }},
		{"ReconcileMissingSources", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.ReconcileMissingSources(l.ID, map[string]struct{}{})
		}},
		{"DeleteMovie", func(s *Store, l Library, m ScanGroupMovie, id int64) error { return s.DeleteMovie(id) }},
		{"DeleteMissingSources", func(s *Store, l Library, m ScanGroupMovie, id int64) error {
			return s.DeleteMissingSources(l.ID, map[string]struct{}{})
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			s, l := scanFixture(t)
			m := scanMovie(l, "/source.strm")
			r := commitScan(t, s, l, "g", []ScanGroupMovie{m}, []ScanGroupSource{{Path: m.Movie.SourcePath}})
			before := scanSnapshot(t, s)
			if _, err := s.db.Exec(`CREATE TRIGGER refuse_invalidation BEFORE DELETE ON scan_groups BEGIN SELECT RAISE(ABORT,'invalidation-fault'); END`); err != nil {
				t.Fatal(err)
			}
			if err := op.run(s, l, m, r.MovieIDs[m.Movie.SourcePath]); err == nil {
				t.Fatal("baseline invalidation fault ignored")
			}
			if scanSnapshot(t, s) != before {
				t.Fatal("baseline invalidation failure partially committed")
			}
			if _, err := s.db.Exec(`DROP TRIGGER refuse_invalidation`); err != nil {
				t.Fatal(err)
			}
			if err := op.run(s, l, m, r.MovieIDs[m.Movie.SourcePath]); err != nil {
				t.Fatal(err)
			}
			groups, err := s.LoadScanGroups(l.ID)
			if err != nil || len(groups) != 0 {
				t.Fatalf("stale groups=%+v %v", groups, err)
			}
		})
	}
	t.Run("userdata-keeps-baseline", func(t *testing.T) {
		s, l := scanFixture(t)
		m := scanMovie(l, "/a")
		r := commitScan(t, s, l, "g", []ScanGroupMovie{m}, []ScanGroupSource{{Path: m.Movie.SourcePath}})
		before, e := s.LoadScanGroups(l.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e := s.SavePlaybackForUser(1, r.MovieIDs[m.Movie.SourcePath], 1, 2, "date", 0); e != nil {
			t.Fatal(e)
		}
		after, e := s.LoadScanGroups(l.ID)
		if e != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("userdata invalidated: %+v %v", after, e)
		}
	})
}

func TestScanSplitGroupPreservesRetainedIdentity(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			s, lib := scanFixture(t)
			a, b := scanMovie(lib, "/CD1"), scanMovie(lib, "/CD2")
			if manual {
				b.Movie.Status = "manual"
			}
			first := commitScan(t, s, lib, "old", []ScanGroupMovie{a, b}, []ScanGroupSource{{Path: a.Movie.SourcePath}, {Path: b.Movie.SourcePath}})
			id := first.MovieIDs[b.Movie.SourcePath]
			if err := s.SavePlaybackForUser(1, id, 42, 7, "date", 9); err != nil {
				t.Fatal(err)
			}
			var created string
			if err := s.db.QueryRow("SELECT created_at FROM movies WHERE id=?", id).Scan(&created); err != nil {
				t.Fatal(err)
			}
			replacement := commitScan(t, s, lib, "old", []ScanGroupMovie{a}, []ScanGroupSource{{Path: a.Movie.SourcePath}})
			if replacement.Deleted != 0 {
				t.Fatal("another group's retained source was deleted")
			}
			second := commitScan(t, s, lib, "new", []ScanGroupMovie{b}, []ScanGroupSource{{Path: b.Movie.SourcePath}})
			if second.MovieIDs[b.Movie.SourcePath] != id {
				t.Fatalf("split lost ID: %+v", second)
			}
			var after string
			var plays int
			if err := s.db.QueryRow("SELECT m.created_at,u.play_count FROM movies m JOIN userdata u ON u.movie_id=m.id AND u.user_id=1 WHERE m.id=?", id).Scan(&after, &plays); err != nil || after != created || plays != 7 {
				t.Fatalf("identity/userdata lost: %s %d %v", after, plays, err)
			}
		})
	}
}

func TestScanCancelledWhileWaitingWriterDoesNotDelete(t *testing.T) {
	for _, sqliteWriter := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			t.Run(fmt.Sprintf("sqlite=%t/group=%t", sqliteWriter, group), func(t *testing.T) {
				s, lib := scanFixture(t)
				m := scanMovie(lib, "/a")
				commitScan(t, s, lib, "g", []ScanGroupMovie{m}, []ScanGroupSource{{Path: m.Movie.SourcePath}})
				before := scanSnapshot(t, s)
				var release func()
				if sqliteWriter {
					tx, err := s.db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					if err := reserveScanWriterTx(tx); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					release = func() {
						if err := tx.Rollback(); err != nil {
							t.Error(err)
						}
					}
				} else {
					s.writeMu.Lock()
					release = s.writeMu.Unlock
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					close(entered)
					if group {
						r, err := s.CommitScanGroupCheckedContext(ctx, lib, "g", "next", 1, "skipped", nil, []ScanGroupSource{{Path: m.Movie.SourcePath}})
						if len(r.MovieIDs) != 0 || r.Added+r.Updated+r.Deleted != 0 {
							done <- fmt.Errorf("cancelled group leaked result: %+v", r)
							return
						}
						done <- err
					} else {
						r, err := s.ReconcileScanInventoryCheckedContext(ctx, lib, map[string]int64{}, nil)
						if r != (ScanReconcileResult{}) {
							done <- fmt.Errorf("cancelled cleanup leaked result: %+v", r)
							return
						}
						done <- err
					}
				}()
				<-entered
				cancel()
				release()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel=%v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("cancelled writer did not finish")
				}
				if scanSnapshot(t, s) != before {
					t.Fatal("cancelled writer deleted or changed index/baseline")
				}
			})
		}
	}
}

func TestScanManualPendingUpdatesStructurePreservesMetadata(t *testing.T) {
	s, lib := scanFixture(t)
	m := scanMovie(lib, "/CD1")
	m.Movie.Status = "manual"
	m.Movie.Plot = "Manual plot"
	m.Movie.SourceProtocol = "http"
	m.Movie.SourceContainer = "mp4"
	first := commitScan(t, s, lib, "g", []ScanGroupMovie{m}, []ScanGroupSource{{Path: m.Movie.SourcePath, Role: "primary"}})
	id := first.MovieIDs[m.Movie.SourcePath]
	if err := s.SavePlaybackForUser(1, id, 42, 7, "date", 9); err != nil {
		t.Fatal(err)
	}
	protected := func() string {
		return libraryDeleteRows(t, s, `SELECT id,library_id,source_path,status,title,plot,genres,created_at FROM movies ORDER BY id`) + libraryDeleteRows(t, s, `SELECT * FROM actors ORDER BY name`) + libraryDeleteRows(t, s, `SELECT * FROM movie_actors ORDER BY movie_id,actor_name`) + libraryDeleteRows(t, s, `SELECT * FROM movie_features ORDER BY movie_id,kind,value`) + libraryDeleteRows(t, s, `SELECT * FROM userdata ORDER BY user_id,movie_id`)
	}
	before := protected()
	m.Movie.Status = "pending"
	m.Movie.Title = ""
	m.Movie.Plot = ""
	m.Movie.Genres = nil
	m.Actors = nil
	for step, parts := range [][]string{{"/CD2"}, nil} {
		var oldVersion int64
		if err := s.db.QueryRow(`SELECT value FROM kv WHERE key='g:version'`).Scan(&oldVersion); err != nil {
			t.Fatal(err)
		}
		m.Movie.AdditionalParts = parts
		m.Movie.SourceProtocol = "https"
		sources := []ScanGroupSource{{Path: m.Movie.SourcePath, Role: "primary"}}
		for _, p := range parts {
			sources = append(sources, ScanGroupSource{Path: p, Role: "part"})
		}
		r := commitScan(t, s, lib, "g", []ScanGroupMovie{m}, sources)
		if r.Updated != 1 || r.Added+r.Deleted != 0 || r.MovieIDs[m.Movie.SourcePath] != id {
			t.Fatalf("step=%d result=%+v", step, r)
		}
		if s.Version("g:version") != fmt.Sprint(oldVersion+1) || storedVersion(t, s, "g:version") != fmt.Sprint(oldVersion+1) {
			t.Fatal("structural change did not publish exactly one version")
		}
		if protected() != before {
			t.Fatal("structural refresh changed manual metadata, actors, features or identity/userdata")
		}
		var protocol string
		if err := s.db.QueryRow(`SELECT source_protocol FROM movies WHERE id=?`, id).Scan(&protocol); err != nil || protocol != "https" {
			t.Fatalf("protocol=%s %v", protocol, err)
		}
		// Simulate the store work of the next unchanged pass: evidence must match
		// the committed baseline, and every row-write trigger rejects the cleanup.
		var triggers []string
		for _, table := range []string{"libraries", "movies", "actors", "movie_actors", "movie_features", "userdata", "kv", "scan_groups", "scan_group_sources"} {
			for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
				name := "manual_no_" + table + "_" + op
				triggers = append(triggers, name)
				if _, err := s.db.Exec(fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'manual-unchanged-dml'); END", name, op, table)); err != nil {
					t.Fatal(err)
				}
			}
		}
		snapshot := scanSnapshot(t, s)
		evidence, err := s.LoadScanIndexEvidence(lib.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(evidence[m.Movie.SourcePath].AdditionalParts, parts) {
			t.Fatalf("stale additional parts: %+v want %v", evidence, parts)
		}
		groups, err := s.LoadScanGroups(lib.ID)
		if err != nil || len(groups["g"].Sources) != len(sources) {
			t.Fatalf("group=%+v err=%v", groups, err)
		}
		cleanup, err := s.ReconcileScanInventoryChecked(lib, r.MovieIDs, []string{"g"})
		if err != nil || cleanup != (ScanReconcileResult{}) {
			t.Fatalf("unchanged cleanup=%+v %v", cleanup, err)
		}
		if scanSnapshot(t, s) != snapshot {
			t.Fatal("unchanged store pass changed rows or versions")
		}
		for _, name := range triggers {
			if _, err := s.db.Exec("DROP TRIGGER " + name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestScanLargeBatches(t *testing.T) {
	s, lib := scanFixture(t)
	movies := make([]ScanGroupMovie, 1101)
	sources := make([]ScanGroupSource, 1101)
	for i := range movies {
		movies[i] = scanMovie(lib, fmt.Sprintf("/source/%d.strm", i))
		sources[i] = ScanGroupSource{Path: movies[i].Movie.SourcePath}
	}
	r := commitScan(t, s, lib, "large", movies, sources)
	if r.Added != len(movies) || len(r.MovieIDs) != len(movies) {
		t.Fatalf("group=%+v", r)
	}
	cleaned, err := s.ReconcileScanInventoryChecked(lib, map[string]int64{}, []string{})
	if err != nil || cleaned.DeletedMovies != len(movies) || cleaned.DeletedGroups != 1 {
		t.Fatalf("cleanup=%+v err=%v", cleaned, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM userdata`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade=%d %v", n, err)
	}
}

func TestScanCrossLibraryMoveInvalidatesPreviousBaseline(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprint(indexed), func(t *testing.T) {
			s, lib := scanFixture(t)
			m := scanMovie(lib, "/shared.strm")
			r := commitScan(t, s, lib, "old", []ScanGroupMovie{m}, []ScanGroupSource{{Path: m.Movie.SourcePath}})
			next, err := s.AddLibrary("next", "/next")
			if err != nil {
				t.Fatal(err)
			}
			m.Movie.LibraryID = next.ID
			var id int64
			if indexed {
				id, err = s.IndexMovie(m.Movie, m.Size, m.Mtime, m.Actors)
			} else {
				id, err = s.UpsertMovie(m.Movie, m.Size, m.Mtime)
			}
			if err != nil || id != r.MovieIDs[m.Movie.SourcePath] {
				t.Fatalf("move=%d %v", id, err)
			}
			groups, err := s.LoadScanGroups(lib.ID)
			if err != nil || len(groups) != 0 {
				t.Fatalf("old baseline=%+v %v", groups, err)
			}
		})
	}
}

func TestScanWriterReservationWithoutRowMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer.db")
	s := versionStore(t, path)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := reserveScanWriterTx(tx); err != nil {
		t.Fatal(err)
	}
	competitor, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()
	// Even a zero-row write on a second connection needs SQLite's writer lock.
	if _, err := competitor.Exec(`UPDATE libraries SET name = name WHERE 0`); !isBusyErr(err) {
		t.Fatalf("writer reservation absent: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := competitor.Exec(`UPDATE libraries SET name = name WHERE 0`); err != nil {
		t.Fatalf("writer lock leaked: %v", err)
	}
}

func TestScanMigrationUpgradeRollbackAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.db")
	s := versionStore(t, path)
	// Model a real v15 database by removing only the v16 artifacts and ledger.
	if _, err := s.db.Exec(`DROP TABLE scan_group_sources; DROP TABLE scan_groups; DELETE FROM schema_migrations WHERE version=16; CREATE TRIGGER fail_scan_migration BEFORE INSERT ON schema_migrations WHEN NEW.version=16 BEGIN SELECT RAISE(ABORT,'migration-fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if failed, err := Open(path); err == nil {
		failed.Close()
		t.Fatal("migration fault accepted")
	} else if !strings.Contains(err.Error(), "migration-fault") {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('scan_groups','scan_group_sources')").Scan(&n); err != nil || n != 0 {
		t.Fatalf("migration leaked tables: %d %v", n, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_scan_migration"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened := versionStore(t, path)
	var checksum string
	if err := reopened.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=16").Scan(&checksum); err != nil || checksum != migration16Checksum() {
		t.Fatalf("ledger=%s %v", checksum, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	versionStore(t, path)
}
