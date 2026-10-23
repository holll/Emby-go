package scraper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

func TestWriteStackedNFOAndRescan(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "GOPJ-559-CD1.strm")
	part := filepath.Join(dir, "GOPJ-559-CD2.strm")
	for _, path := range []string{source, part} {
		if err := os.WriteFile(path, []byte("https://media.test/movie.mp4\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := db.AddLibrary("test", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(db, lib); err != nil {
		t.Fatal(err)
	}
	id, err := db.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	if movie.NFOPath != "" || len(movie.AdditionalParts) != 1 {
		t.Fatalf("fixture=%+v", movie)
	}
	if err := writeNFO(movie, nfo.ScrapeFields{Title: "Shared title", Number: "GOPJ-559"}); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, "GOPJ-559.nfo")
	if _, err := nfo.Read(shared); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "GOPJ-559-CD1.nfo")); !os.IsNotExist(err) {
		t.Fatalf("unexpected CD1 NFO: %v", err)
	}
	if _, err := scanner.RescanOne(db, lib, part); err != nil {
		t.Fatal(err)
	}
	movie, err = db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	if movie.NFOPath != shared || movie.Title != "Shared title" || len(movie.AdditionalParts) != 1 {
		t.Fatalf("reindexed movie=%+v", movie)
	}
}

func TestWriteNFOKeepsRegisteredPath(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "GOPJ-559-CD1.nfo")
	if err := os.WriteFile(legacy, []byte("<movie><title>Old</title><custom>Keep</custom></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	movie := store.Movie{SourcePath: filepath.Join(dir, "GOPJ-559-CD1.strm"), NFOPath: legacy,
		AdditionalParts: []string{filepath.Join(dir, "GOPJ-559-CD2.strm")}}
	if err := writeNFO(movie, nfo.ScrapeFields{Title: "Updated", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<title>Updated</title>") || !strings.Contains(string(raw), "<custom>Keep</custom>") {
		t.Fatalf("legacy content=%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "GOPJ-559.nfo")); !os.IsNotExist(err) {
		t.Fatalf("unexpected migration: %v", err)
	}
}
