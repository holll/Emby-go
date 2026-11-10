package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAutoMarksRoundTripAndScanPreservation(t *testing.T) {
	s, lib := scanFixture(t)
	entry := scanMovie(lib, "/fixture/A.strm")
	entry.Movie.Tags = []string{"高清", "自动已有但不可推断"}
	entry.Movie.AutoMarks = []string{"must not infer"}
	result := commitScan(t, s, lib, "g", []ScanGroupMovie{entry}, []ScanGroupSource{{Path: entry.Movie.SourcePath}})
	id := result.MovieIDs[entry.Movie.SourcePath]
	got, err := s.Movie(id)
	if err != nil || len(got.AutoMarks) != 0 {
		t.Fatalf("scan invented ownership: %+v err=%v", got, err)
	}
	if err := s.SetScrapeResult(id, "keep scrape error"); err != nil {
		t.Fatal(err)
	}
	marks := []string{"高清", "字幕", "a\"b"}
	global, _ := strconv.Atoi(s.Version("g:version"))
	key := fmt.Sprintf("lib:%d:version", lib.ID)
	local, _ := strconv.Atoi(s.Version(key))
	if err := s.SetAutoMarks(id, marks); err != nil {
		t.Fatal(err)
	}
	if s.Version("g:version") != strconv.Itoa(global+1) || s.Version(key) != strconv.Itoa(local+1) {
		t.Fatal("ownership write did not advance both versions")
	}
	entry.Movie.AutoMarks = []string{"stale scanner ownership"}
	entry.Movie.Title = "rescanned"
	commitScan(t, s, lib, "g", []ScanGroupMovie{entry}, []ScanGroupSource{{Path: entry.Movie.SourcePath}})
	if _, err := s.IndexMovie(entry.Movie, entry.Size, entry.Mtime, nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.Movie(id)
	if err != nil || !slices.Equal(got.AutoMarks, marks) || got.Title != "rescanned" || got.LastScrapeError != "keep scrape error" || !slices.Equal(got.Tags, entry.Movie.Tags) {
		t.Fatalf("preservation: %+v err=%v", got, err)
	}
	byIDs, err := s.MoviesByIDs([]int64{id})
	if err != nil || !slices.Equal(byIDs[id].AutoMarks, marks) {
		t.Fatalf("batch read=%+v err=%v", byIDs, err)
	}
	for _, empty := range [][]string{nil, {}} {
		if err := s.SetAutoMarks(id, empty); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := s.db.QueryRow("SELECT auto_marks FROM movies WHERE id=?", id).Scan(&raw); err != nil || raw != "[]" {
			t.Fatalf("empty raw=%q err=%v", raw, err)
		}
	}
	// Ordinary inserts ignore provenance even when a caller supplies tag-like marks.
	entry.Movie.SourcePath = "/fixture/B.strm"
	newID, err := s.IndexMovie(entry.Movie, entry.Size, entry.Mtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Movie(newID)
	if err != nil || len(got.AutoMarks) != 0 {
		t.Fatalf("upsert invented ownership: %+v err=%v", got, err)
	}
	if _, err := s.db.Exec("UPDATE movies SET auto_marks='not-json' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	got, err = s.Movie(id)
	if err != nil || len(got.AutoMarks) != 0 || got.Title != "rescanned" {
		t.Fatalf("corrupt display fallback=%+v err=%v", got, err)
	}
	report, err := s.CorruptedJSON(10)
	if err != nil || report.Total != 1 || len(report.Items) != 1 || report.Items[0].MovieID != id || report.Items[0].Column != "auto_marks" {
		t.Fatalf("diagnostics=%+v err=%v", report, err)
	}
}

func TestAutoMarksRollbackAndMissingMovie(t *testing.T) {
	for _, stage := range []string{"global", "library", "update", "missing"} {
		t.Run(stage, func(t *testing.T) {
			s, lib := scanFixture(t)
			entry := scanMovie(lib, "/fixture/A.strm")
			id, err := s.IndexMovie(entry.Movie, entry.Size, entry.Mtime, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetAutoMarks(id, []string{"old"}); err != nil {
				t.Fatal(err)
			}
			triggers := map[string]string{
				"global":  "BEFORE INSERT ON kv WHEN NEW.key='g:version'",
				"library": "BEFORE INSERT ON kv WHEN NEW.key LIKE 'lib:%:version'",
				"update":  "BEFORE UPDATE OF auto_marks ON movies",
			}
			if stage != "missing" {
				if _, err := s.db.Exec("CREATE TRIGGER fail_marks " + triggers[stage] + " BEGIN SELECT RAISE(ABORT,'marks-fault'); END"); err != nil {
					t.Fatal(err)
				}
			} else {
				id = -1
			}
			before := scanSnapshot(t, s)
			global := s.Version("g:version")
			err = s.SetAutoMarks(id, []string{"new"})
			if err == nil || (stage == "missing" && !errors.Is(err, sql.ErrNoRows)) || (stage != "missing" && !strings.Contains(err.Error(), "marks-fault")) {
				t.Fatalf("fault=%v", err)
			}
			if after := scanSnapshot(t, s); after != before || s.Version("g:version") != global {
				t.Fatal("failed ownership write leaked row or version changes")
			}
		})
	}
}

func TestAutoMarksMigrationUpgradeRollbackAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marks.db")
	s := versionStore(t, path)
	lib, err := s.AddLibrary("fixture", "/fixture")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.IndexMovie(Movie{LibraryID: lib.ID, SourcePath: "/fixture/A.strm", Status: "success", Title: "keep", Tags: []string{"高清"}, ExtraBackdrops: []string{"extra.jpg"}}, 10, time.Unix(1234, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	if err := s.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=17").Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE movies DROP COLUMN auto_marks; DELETE FROM schema_migrations WHERE version=18;
CREATE TRIGGER fail_marks_migration BEFORE INSERT ON schema_migrations WHEN NEW.version=18 BEGIN SELECT RAISE(ABORT,'marks-migration-fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if failed, err := Open(path); err == nil {
		failed.Close()
		t.Fatal("migration fault accepted")
	} else if !strings.Contains(err.Error(), "marks-migration-fault") {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	columns, err := tableColumns(db, "movies")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := columns["auto_marks"]; exists {
		t.Fatal("failed migration leaked its column")
	}
	if _, err := db.Exec("DROP TRIGGER fail_marks_migration"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened := versionStore(t, path)
		var checksum, prior, raw string
		if err := reopened.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=18").Scan(&checksum); err != nil || checksum != migration18Checksum() {
			t.Fatalf("new ledger=%q err=%v", checksum, err)
		}
		if err := reopened.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=17").Scan(&prior); err != nil || prior != previous {
			t.Fatalf("migration17 modified: %q err=%v", prior, err)
		}
		if err := reopened.db.QueryRow("SELECT auto_marks FROM movies WHERE id=?", id).Scan(&raw); err != nil || raw != "[]" {
			t.Fatalf("migration inferred marks=%q err=%v", raw, err)
		}
		movie, err := reopened.Movie(id)
		if err != nil || movie.Title != "keep" || !slices.Equal(movie.Tags, []string{"高清"}) || !slices.Equal(movie.ExtraBackdrops, []string{"extra.jpg"}) {
			t.Fatalf("legacy row lost: %+v err=%v", movie, err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("UPDATE schema_migrations SET checksum='modified' WHERE version=18"); err != nil {
		t.Fatal(err)
	}
	if modified, err := Open(path); err == nil {
		modified.Close()
		t.Fatal("modified migration checksum accepted")
	} else if !strings.Contains(err.Error(), "unsupported or modified schema migration 18") {
		t.Fatal(err)
	}
}
