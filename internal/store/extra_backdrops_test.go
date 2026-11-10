package store

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExtraBackdropOrderedRoundTripAndDiagnostics(t *testing.T) {
	s, libID := newFeatureStore(t)
	m := Movie{LibraryID: libID, SourcePath: "/fixture/A.strm", Status: "success", Title: "retained", BackdropPath: "/fixture/fanart.webp"}
	mtime := time.Unix(1234, 0)
	id, err := s.IndexMovie(m, 10, mtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{{"/fixture/extrafanart/fanart2.jpg", "/fixture/extrafanart/fanart10.jpg"}, nil} {
		m.ExtraBackdrops = want
		if gotID, err := s.IndexMovie(m, 10, mtime, nil); err != nil || gotID != id {
			t.Fatalf("upsert ID=%d err=%v", gotID, err)
		}
		got, err := s.Movie(id)
		if err != nil || !slices.Equal(got.ExtraBackdrops, want) || got.BackdropPath != m.BackdropPath {
			t.Fatalf("movie=%+v want=%v err=%v", got, want, err)
		}
		var raw string
		if err := s.db.QueryRow("SELECT extra_backdrops FROM movies WHERE id=?", id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if len(want) == 0 && raw != "[]" {
			t.Fatalf("empty extras encoded as %q", raw)
		}
	}
	if _, err := s.db.Exec("UPDATE movies SET extra_backdrops='not-json' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	got, err := s.Movie(id)
	if err != nil || len(got.ExtraBackdrops) != 0 || got.Title != m.Title {
		t.Fatalf("corrupt display fallback=%+v err=%v", got, err)
	}
	report, err := s.CorruptedJSON(10)
	if err != nil || report.Total != 1 || len(report.Items) != 1 || report.Items[0].MovieID != id || report.Items[0].Column != "extra_backdrops" {
		t.Fatalf("diagnostic=%+v err=%v", report, err)
	}
}

func TestExtraBackdropMigrationUpgradeRollbackAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extra.db")
	s := versionStore(t, path)
	lib, err := s.AddLibrary("fixture", "/fixture")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.IndexMovie(Movie{LibraryID: lib.ID, SourcePath: "/fixture/A.strm", Status: "success", Title: "keep", BackdropPath: "/fixture/fanart.webp"}, 10, time.Unix(1234, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	if err := s.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=16").Scan(&previous); err != nil {
		t.Fatal(err)
	}
	// Synthetic v16 fixture: remove only the new column and ledger entry.
	if _, err := s.db.Exec(`ALTER TABLE movies DROP COLUMN extra_backdrops; DELETE FROM schema_migrations WHERE version=17;
CREATE TRIGGER fail_extra_migration BEFORE INSERT ON schema_migrations WHEN NEW.version=17 BEGIN SELECT RAISE(ABORT,'extra-migration-fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if failed, err := Open(path); err == nil {
		failed.Close()
		t.Fatal("migration fault accepted")
	} else if !strings.Contains(err.Error(), "extra-migration-fault") {
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
	if _, exists := columns["extra_backdrops"]; exists {
		t.Fatal("failed migration leaked its column")
	}
	if _, err := db.Exec("DROP TRIGGER fail_extra_migration"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened := versionStore(t, path)
		var checksum, prior, raw string
		if err := reopened.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=17").Scan(&checksum); err != nil || checksum != migration17Checksum() {
			t.Fatalf("new ledger=%q err=%v", checksum, err)
		}
		if err := reopened.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=16").Scan(&prior); err != nil || prior != previous {
			t.Fatalf("prior ledger modified: %q err=%v", prior, err)
		}
		if err := reopened.db.QueryRow("SELECT extra_backdrops FROM movies WHERE id=?", id).Scan(&raw); err != nil || raw != "[]" {
			t.Fatalf("migration default=%q err=%v", raw, err)
		}
		movie, err := reopened.Movie(id)
		if err != nil || movie.Title != "keep" || movie.BackdropPath != "/fixture/fanart.webp" {
			t.Fatalf("legacy row lost: %+v err=%v", movie, err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("UPDATE schema_migrations SET checksum='modified' WHERE version=17"); err != nil {
		t.Fatal(err)
	}
	if modified, err := Open(path); err == nil {
		modified.Close()
		t.Fatal("modified migration checksum accepted")
	} else if !strings.Contains(err.Error(), "unsupported or modified schema migration 17") {
		t.Fatal(err)
	}
}
