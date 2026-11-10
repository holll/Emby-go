package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadFileSizeBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.nfo")
	for _, size := range []int{0, MaxFileSize, MaxFileSize + 1, 2 * MaxFileSize} {
		data := bytes.Repeat([]byte{'x'}, size)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(path)
		if size > MaxFileSize {
			if !errors.Is(err, ErrTooLarge) || got != nil {
				t.Fatalf("size %d: got %d bytes, error %v", size, len(got), err)
			}
		} else if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("size %d: got %d bytes, error %v", size, len(got), err)
		}
	}
}

func TestReadFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.nfo")
	if got, err := ReadFile(missing); !errors.Is(err, os.ErrNotExist) || got != nil {
		t.Fatalf("missing: got %d bytes, error %v", len(got), err)
	}
	if _, err := Read(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read missing: %v", err)
	}
	if got, err := ReadFile(t.TempDir()); err == nil || errors.Is(err, ErrTooLarge) || got != nil {
		t.Fatalf("directory: got %d bytes, error %v", len(got), err)
	}
}

func TestOversizedNFOReadersAndWriters(t *testing.T) {
	const prefix = "<movie><title>old</title><actor><name>actor</name></actor><!--"
	const suffix = "--></movie>"
	path := writeTempNFO(t, prefix+suffix)
	snap, err := ReadTranslationSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	body := prefix + strings.Repeat("x", MaxFileSize-len(prefix)-len(suffix)) + suffix
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if meta, err := Read(path); err != nil || meta.Title != "old" {
		t.Fatalf("exact-limit valid XML: title %q, error %v", meta.Title, err)
	}
	// This stays well-formed XML: rejection must be the byte boundary, not parsing.
	body = prefix + "x" + body[len(prefix):]
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if meta, err := ReadBytes([]byte(body)); err != nil || meta.Title != "old" {
		t.Fatalf("already-read bytes must not duplicate file size validation: %v", err)
	}
	stamp := time.Unix(123456789, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"Read", func() error { _, err := Read(path); return err }},
		{"TagPreview", func() error { _, err := TagPreview(path, []string{"title"}); return err }},
		{"ReadTranslationSnapshot", func() error { _, err := ReadTranslationSnapshot(path); return err }},
		{"UpdateScraped", func() error { return UpdateScraped(path, ScrapeFields{Title: "new", Overwrite: true}) }},
		{"UpdateScrapedWithAutoMarks", func() error {
			owned, err := UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: true}, []string{"C"}, []string{"UC"})
			if owned != nil {
				t.Errorf("failed update returned ownership: %v", owned)
			}
			return err
		}},
		{"SaveFileInfo", func() error { return SaveFileInfo(path, FileInfoMeta{}, &StreamDetails{}) }},
		{"SetActorThumb", func() error {
			changed, err := SetActorThumb(path, "actor", "new.jpg")
			if changed {
				t.Error("oversized actor update reported changed")
			}
			return err
		}},
		{"CommitTranslation", func() error {
			changed, err := CommitTranslation(context.Background(), path, snap, "new", "")
			if changed {
				t.Error("oversized translation reported changed")
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("got %v, want ErrTooLarge", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != body {
				t.Fatalf("oversized source changed: %v", err)
			}
			after, err := os.Stat(path)
			if err != nil || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("source modification time changed: %v", err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("rejected read left temporary files: %v, %v", entries, err)
			}
		})
	}
}
