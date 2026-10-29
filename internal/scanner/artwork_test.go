package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"emby-go/internal/store"
)

// A private cover survives both full and incremental scans without an NFO.
// Two unrelated STRMs in one directory must never share the private override.
func TestPrivateArtworkSurvivesScanWithoutNFO(t *testing.T) {
	for _, mode := range []ScanMode{ScanModeFull, ScanModeIncremental} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			s := newStore(t, root)
			lib, err := s.AddLibrary("artwork", root)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"A.strm", "B.strm"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("https://media.test/video.mp4\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			shared := filepath.Join(root, "poster.jpg")
			if err := os.WriteFile(shared, []byte("existing shared cover"), 0600); err != nil {
				t.Fatal(err)
			}
			scan := func() Result {
				r, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: mode}, nil)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			scan()
			movies, err := s.MoviesForScrape(lib.ID, false, 0)
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]store.Movie{}
			for _, movie := range movies {
				byName[filepath.Base(movie.SourcePath)] = movie
			}
			a, b := byName["A.strm"], byName["B.strm"]
			if a.ID == 0 || b.ID == 0 {
				t.Fatal("missing pending movies")
			}
			if a.PosterPath != shared || b.PosterPath != shared {
				t.Fatalf("shared fallback: A=%q B=%q", a.PosterPath, b.PosterPath)
			}
			private := filepath.Join(root, "A.strm.poster.jpg")
			if err := os.WriteFile(private, []byte("private cover"), 0600); err != nil {
				t.Fatal(err)
			}
			scan()
			afterA, err := s.Movie(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterB, err := s.Movie(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if afterA.PosterPath != private || afterB.PosterPath != shared || afterA.Status != "pending" || afterA.ID != a.ID || afterB.ID != b.ID {
				t.Fatalf("private cover or metadata changed: A=%+v B=%+v", afterA, afterB)
			}
			r := scan()
			if mode == ScanModeIncremental && (r.UnchangedGroups != 2 || r.IndexChanged) {
				t.Fatalf("stable artwork must not write: %+v", r)
			}
			if _, err := os.Stat(filepath.Join(root, "A.nfo")); !os.IsNotExist(err) {
				t.Fatalf("unexpected NFO: %v", err)
			}
			if err := os.Remove(private); err != nil {
				t.Fatal(err)
			}
			scan()
			afterA, err = s.Movie(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if afterA.PosterPath != shared {
				t.Fatalf("private removal must restore shared fallback: %+v", afterA)
			}
		})
	}
}

func TestPrivateArtworkSnapshotDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "A.strm.poster.jpg")); err != nil {
		t.Skip(err)
	}
	images, err := snapshotImages(root, filepath.Join(root, "A.strm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range images {
		if img.Kind == "poster" && img.Present {
			t.Fatalf("private symlink accepted: %+v", img)
		}
	}
}
