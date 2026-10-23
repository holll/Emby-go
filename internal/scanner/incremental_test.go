package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/store"
)

// The fingerprint hashes .strm content, so a change that preserves size and
// mtime must still rebuild the group; a second pass must then be unchanged.
func TestIncrementalDetectsContentChangeWithSameStat(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "ABC.strm")
	if err := os.WriteFile(source, []byte("https://media.test/a.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ABC.nfo"), []byte("<movie><title>Same</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("https://media.test/b.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	result, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.UnchangedGroups != 0 || !result.IndexChanged {
		t.Fatalf("content change with identical stat must rebuild: %+v", result)
	}
	result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.UnchangedGroups != 1 || result.Added+result.Updated+result.Deleted != 0 || result.IndexChanged {
		t.Fatalf("second incremental must be unchanged: %+v", result)
	}
}

func TestIncrementalCDGroupMergeAndSplit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Film-CD1.strm", "Film-CD2.strm"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("https://media.test/"+name+".mp4\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Film-CD1.nfo"), []byte("<movie><title>Film</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	movies := visible(t, s)
	if len(movies) != 1 || len(movies[0].AdditionalParts) != 1 {
		t.Fatalf("initial stacked group: %+v", movies)
	}

	if err := os.Remove(filepath.Join(root, "Film-CD2.strm")); err != nil {
		t.Fatal(err)
	}
	result, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Deleted != 0 {
		t.Fatalf("split should rewrite the primary only: %+v", result)
	}
	movies = visible(t, s)
	if len(movies) != 1 || len(movies[0].AdditionalParts) != 0 {
		t.Fatalf("after removing CD2: %+v", movies)
	}

	if err := os.WriteFile(filepath.Join(root, "Film-CD2.strm"), []byte("https://media.test/Film-CD2.strm.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 {
		t.Fatalf("merge should rewrite the primary: %+v", result)
	}
	movies = visible(t, s)
	if len(movies) != 1 || len(movies[0].AdditionalParts) != 1 {
		t.Fatalf("after restoring CD2: %+v", movies)
	}
	result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.UnchangedGroups != 1 || result.IndexChanged {
		t.Fatalf("steady state must be unchanged: %+v", result)
	}
}

// Any external index writer must invalidate the group baseline, otherwise a
// later incremental scan would skip a row that no longer matches its NFO.
func TestIncrementalRebuildsAfterExternalIndexWrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "A.strm")
	if err := os.WriteFile(source, []byte("https://media.test/a.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "A.nfo"), []byte("<movie><title>FromNFO</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: source, Title: "External", Status: "success"}, 1, time.Unix(1, 0), nil); err != nil {
		t.Fatal(err)
	}
	result, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.UnchangedGroups != 0 {
		t.Fatalf("invalidated group must rebuild: %+v", result)
	}
	id, err := s.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := s.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	if movie.Title != "FromNFO" {
		t.Fatalf("NFO metadata should win after rebuild, got %q", movie.Title)
	}
}

func TestIncrementalCancellationKeepsPartialResultAndSkipsCleanup(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"A.strm", "B.strm"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("https://media.test/"+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+".nfo"), []byte("<movie><title>"+name+"</title></movie>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := ScanWithOptions(ctx, s, lib, ScanOptions{Mode: ScanModeFull}, func(p Progress) {
		if p.Done >= 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if result.GroupsDone != 1 || !result.Incomplete || result.CleanupPerformed {
		t.Fatalf("partial cancellation result: %+v", result)
	}
	if _, err := s.MovieIDByPath(filepath.Join(root, "A.strm")); err != nil {
		t.Fatalf("first committed group should survive: %v", err)
	}
}
