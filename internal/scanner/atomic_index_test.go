package scanner

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestScanPublishesCommittedItemsBeforeLaterFailure(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"a.strm": "https://media.test/a.mp4", "b.strm": "https://media.test/b.mp4",
		"a.nfo": "<movie><title>A</title><actor><name>ActorA</name></actor></movie>",
		"b.nfo": "<movie><title>B</title><actor><name>ActorB</name></actor></movie>",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("Fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "missing.strm"), Title: "RetainOnError", Status: "success"}, 0, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", filepath.Join(root, "test.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`CREATE TRIGGER fail_second_index BEFORE INSERT ON movies WHEN NEW.title='B' BEGIN SELECT RAISE(ABORT,'fixture-second-index'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(s, lib)
	if err == nil || !strings.Contains(err.Error(), "fixture-second-index") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Success != 1 || result.Pending != 0 {
		t.Fatalf("uncommitted row counted as success: %+v", result)
	}
	if s.Version("g:version") != "2" {
		t.Fatalf("first item not published or failed item published: %s", s.Version("g:version"))
	}
	a, err := s.MovieIDByPath(filepath.Join(root, "a.strm"))
	if err != nil {
		t.Fatal(err)
	}
	actors, err := s.Actors(a)
	if err != nil || len(actors) != 1 || actors[0].Name != "ActorA" {
		t.Fatalf("first item actors=%+v err=%v", actors, err)
	}
	if _, err := s.MovieIDByPath(filepath.Join(root, "b.strm")); !store.NotFound(err) {
		t.Fatalf("failed item persisted: %v", err)
	}
	if _, err := s.Movie(stale); err != nil {
		t.Fatalf("incomplete scan deleted missing source: %v", err)
	}
	if _, err := fixture.Exec("DROP TRIGGER fail_second_index"); err != nil {
		t.Fatal(err)
	}
	result, err = Scan(s, lib)
	if err != nil || result.Success != 2 {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	if _, err := s.Movie(stale); !store.NotFound(err) {
		t.Fatalf("complete scan did not reconcile missing source: %v", err)
	}
}

func TestScanReadFailureDoesNotDeleteMissingSources(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"a.strm": "https://media.test/a.mp4", "a.nfo": "<movie><title>A</title></movie>",
		"aa.strm": "ed2k://unsupported",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	missingTarget := filepath.Join(root, "unreadable-target")
	if err := os.Symlink(missingTarget, filepath.Join(root, "b.strm")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("Fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "missing.strm"), Status: "success", Title: "Retain"}, 0, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := s.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "aa.strm"), Status: "success", Title: "Old protocol"}, 0, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Scan(s, lib)
	if err == nil || result.Failed != 1 || result.Success != 1 || result.Skipped != 1 {
		t.Fatalf("read failure reported complete: %+v %v", result, err)
	}
	if _, err := s.Movie(stale); err != nil {
		t.Fatalf("incomplete read deleted missing source: %v", err)
	}
	if _, err := s.Movie(protocol); !store.NotFound(err) {
		t.Fatalf("protocol change not reconciled: %v", err)
	}
	if s.Version("g:version") != "3" {
		t.Fatalf("committed protocol removal not published despite later read failure: %s", s.Version("g:version"))
	}
	// Make the symlink readable without deleting/replacing it, then retry.
	if err := os.WriteFile(missingTarget, []byte("https://media.test/b.mp4"), 0644); err != nil {
		t.Fatal(err)
	}
	if result, err := Scan(s, lib); err != nil || result.Failed != 0 {
		t.Fatalf("read retry failed: %+v %v", result, err)
	}
	if _, err := s.Movie(stale); !store.NotFound(err) {
		t.Fatalf("complete retry did not clean missing source: %v", err)
	}
}
