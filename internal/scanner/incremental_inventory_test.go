package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"emby-go/internal/store"
)

func TestIncrementalRestoredDirectoryTimestampCannotAuthorizeCleanup(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Film-CD1.strm", "Film-CD2.strm"} {
		scanWrite(t, filepath.Join(root, name), "https://media.test/a.mp4")
	}
	scanWrite(t, filepath.Join(root, "Film-CD1.nfo"), "<movie><title>Film</title></movie>")
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	var added bool
	got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, func(p Progress) {
		if p.Done != 0 || added {
			return
		}
		added = true
		info, e := os.Stat(root)
		if e != nil {
			t.Fatal(e)
		}
		scanWrite(t, filepath.Join(root, "Film-CD3.strm"), "https://media.test/3.mp4")
		if e := os.Chtimes(root, info.ModTime(), info.ModTime()); e != nil {
			t.Fatal(e)
		}
	})
	if !errors.Is(err, ErrScanIncomplete) || !got.Incomplete || got.CleanupPerformed {
		t.Fatalf("restored directory mtime authorized cleanup: %+v %v", got, err)
	}
	got, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil || got.Updated != 1 {
		t.Fatalf("retry should repair added member: %+v %v", got, err)
	}
	id, err := s.MovieIDByPath(filepath.Join(root, "Film-CD1.strm"))
	if err != nil {
		t.Fatal(err)
	}
	movie, err := s.Movie(id)
	if err != nil || len(movie.AdditionalParts) != 2 {
		t.Fatalf("missing member after retry %+v %v", movie, err)
	}
}

func TestSnapshotImageReselectionIgnoresDirectoryStatCache(t *testing.T) {
	root := t.TempDir()
	scanWrite(t, filepath.Join(root, "Film-CD1.strm"), "https://media.test/a.mp4")
	scanWrite(t, filepath.Join(root, "Film-CD2.strm"), "https://media.test/b.mp4")
	scanWrite(t, filepath.Join(root, "folder.jpg"), "old-image")
	lib := store.Library{Path: root}
	groups, err := enumerateGroups(lib)
	if err != nil {
		t.Fatal(err)
	}
	for key, group := range groups {
		inputs, err := readGroupInputs(lib, key, group)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		scanWrite(t, filepath.Join(root, "poster.webp"), "new-image")
		if err := os.Chtimes(root, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if inputs.stillMatches() {
			t.Fatal("directory cache hid new higher-priority poster")
		}
	}
}
