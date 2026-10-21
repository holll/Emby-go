package store

import (
	"reflect"
	"testing"
)

func TestDeleteIndexedMovieSnapshotNullTitle(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	if _, err := f.s.db.Exec("UPDATE movies SET title=NULL WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	before := f.s.gversion.Load()
	movie, err := f.s.DeleteIndexedMovieSnapshot(f.victim)
	if err != nil || movie.ID != f.victim || movie.Title != "" || movie.SourcePath == "" || movie.LibraryID != f.lib {
		t.Fatalf("null title delete: %+v %v", movie, err)
	}
	if f.s.gversion.Load() != before+1 {
		t.Fatal("null title deletion did not publish version")
	}
	if _, err := f.s.Movie(f.victim); !NotFound(err) {
		t.Fatal("deleted movie still exists", err)
	}
}

func TestDeleteIndexedMovieSnapshotOnlyCommittedFields(t *testing.T) {
	for _, fault := range []string{"none", "library", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			if _, err := f.s.db.Exec("UPDATE movies SET title=?,source_path=? WHERE id=?", "最新标题", "/fixture/new-path.strm", f.victim); err != nil {
				t.Fatal(err)
			}
			trigger := ""
			if fault == "library" {
				trigger = `CREATE TRIGGER snapshot_fault BEFORE INSERT ON kv WHEN NEW.key LIKE 'lib:%:version' BEGIN SELECT RAISE(ABORT,'snapshot-canary'); END`
			}
			if fault == "commit" {
				trigger = `CREATE TABLE snapshot_parent(id INTEGER PRIMARY KEY); CREATE TABLE snapshot_child(id INTEGER REFERENCES snapshot_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER snapshot_fault AFTER DELETE ON movies BEGIN INSERT INTO snapshot_child VALUES(99); END`
			}
			if trigger != "" {
				if _, err := f.s.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.s.DeleteIndexedMovieSnapshot(f.victim)
			if fault != "none" {
				if err == nil || !reflect.DeepEqual(result, Movie{}) {
					t.Fatalf("uncommitted snapshot escaped: %+v %v", result, err)
				}
				if _, err := f.s.Movie(f.victim); err != nil {
					t.Fatal("failed delete lost movie", err)
				}
				if _, err := f.s.db.Exec("DROP TRIGGER snapshot_fault"); err != nil {
					t.Fatal(err)
				}
				result, err = f.s.DeleteIndexedMovieSnapshot(f.victim)
			}
			expected := Movie{ID: f.victim, LibraryID: f.lib, Title: "最新标题", SourcePath: "/fixture/new-path.strm"}
			if err != nil || !reflect.DeepEqual(result, expected) {
				t.Fatalf("got %+v err=%v want %+v", result, err, expected)
			}
			result, err = f.s.DeleteIndexedMovieSnapshot(f.victim)
			if !NotFound(err) || !reflect.DeepEqual(result, Movie{}) {
				t.Fatal("missing movie produced snapshot")
			}
		})
	}
}
