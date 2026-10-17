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

func TestScanProtocolDeletionPublishesBeforeLaterIndexFailure(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"a.strm": "ed2k://unsupported", "b.strm": "https://media.test/b.mp4", "b.nfo": "<movie><title>B</title></movie>"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("Fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 2)
	for _, name := range []string{"a.strm", "missing.strm"} {
		id, err := s.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, name), Title: name, Status: "success", Genres: []string{"Genre"}}, 0, time.Unix(1, 0), []store.ActorRef{{Name: "Actor"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	fixture, err := sql.Open("sqlite", filepath.Join(root, "test.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`CREATE TRIGGER fail_after_protocol BEFORE INSERT ON movies WHEN NEW.title='B' BEGIN SELECT RAISE(ABORT,'fixture-after-protocol'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(s, lib)
	if err == nil || !strings.Contains(err.Error(), "fixture-after-protocol") || result.Skipped != 1 || result.Success != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := s.Movie(ids[0]); !store.NotFound(err) {
		t.Fatalf("protocol removal missing: %v", err)
	}
	if _, err := s.Movie(ids[1]); err != nil {
		t.Fatalf("failed scan reconciled missing inventory: %v", err)
	}
	var durable string
	if err := fixture.QueryRow("SELECT value FROM kv WHERE key='g:version'").Scan(&durable); err != nil {
		t.Fatal(err)
	}
	if durable != "3" || s.Version("g:version") != durable {
		t.Fatalf("committed delete not published before later error: db=%s mirror=%s", durable, s.Version("g:version"))
	}
}

func TestScanReconciliationVersionFailurePreservesMissingRows(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"a.strm": "https://media.test/a.mp4", "a.nfo": "<movie><title>A</title></movie>"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("Fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "missing.strm"), Title: "Retain", Status: "success"}, 0, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", filepath.Join(root, "test.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`CREATE TRIGGER fail_reconcile_version BEFORE UPDATE ON kv WHEN NEW.key='g:version' AND CAST(NEW.value AS INTEGER)=3 BEGIN SELECT RAISE(ABORT,'fixture-reconcile-version'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(s, lib)
	if err == nil || !strings.Contains(err.Error(), "fixture-reconcile-version") || result.Success != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := s.Movie(missing); err != nil {
		t.Fatalf("unpublished cleanup deleted missing row: %v", err)
	}
	if s.Version("g:version") != "2" {
		t.Fatalf("failed reconciliation advanced mirror: %s", s.Version("g:version"))
	}
	if _, err := fixture.Exec("DROP TRIGGER fail_reconcile_version"); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Movie(missing); !store.NotFound(err) {
		t.Fatalf("retry did not reconcile: %v", err)
	}
}

func TestRescanProtocolDeletionNeedsNoTailBump(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.strm")
	if err := os.WriteFile(path, []byte("ed2k://unsupported"), 0644); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("Fixture", root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: path, Title: "A", Status: "success"}, 0, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", filepath.Join(root, "test.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`CREATE TRIGGER reject_phantom_bump BEFORE UPDATE ON kv WHEN NEW.key='g:version' AND CAST(NEW.value AS INTEGER)>2 BEGIN SELECT RAISE(ABORT,'fixture-phantom-bump'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := ReindexOne(s, lib, path)
	if err != nil || result.Skipped != 1 || s.Version("g:version") != "1" {
		t.Fatalf("non-destructive result=%+v err=%v version=%s", result, err, s.Version("g:version"))
	}
	if _, err := s.Movie(id); err != nil {
		t.Fatalf("non-destructive recovery deleted: %v", err)
	}
	for i := 0; i < 2; i++ {
		result, err := RescanOne(s, lib, path)
		if err != nil || result.Skipped != 1 || s.Version("g:version") != "2" {
			t.Fatalf("rescan %d result=%+v err=%v version=%s", i, result, err, s.Version("g:version"))
		}
	}
	if _, err := s.Movie(id); !store.NotFound(err) {
		t.Fatalf("protocol record retained: %v", err)
	}
}
