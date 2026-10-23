package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"emby-go/internal/store"
)

// Both entry points must use the same stacking/fallback rules, without moving
// library-only progress reporting or missing-source cleanup into scanGroup.
func TestScanGroupEntryPoints(t *testing.T) {
	for _, mode := range []string{"library", "single", "reindex"} {
		for _, tc := range []struct {
			name   string
			files  []string
			target string
			stack  bool
		}{
			{"stack", []string{"Movie-CD3.strm", "Movie-CD1.strm", "Movie-CD2.strm"}, "Movie-CD2.strm", true},
			{"without CD1", []string{"Movie-CD3.strm", "Movie-CD2.strm"}, "Movie-CD2.strm", false},
			{"single CD1", []string{"Movie-CD1.strm"}, "Movie-CD1.strm", false},
			{"plain", []string{"Movie.strm"}, "Movie.strm", false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				s := newStore(t, root)
				lib, err := s.AddLibrary("test", root)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range tc.files {
					if err := os.WriteFile(filepath.Join(root, name), []byte("https://media.test/"+name+"\n"), 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, name[:len(name)-len(".strm")]+".nfo"), []byte("<movie><title>Movie</title></movie>"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				stalePath := filepath.Join(root, "missing.strm")
				if _, err := s.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: stalePath, OutputDir: root, Title: "Missing", Status: "success"}, 0, time.Time{}); err != nil {
					t.Fatal(err)
				}
				var result Result
				var progress []Progress
				if mode == "library" {
					result, err = ScanWithProgress(s, lib, func(p Progress) { progress = append(progress, p) })
				} else if mode == "reindex" {
					result, err = ReindexOne(s, lib, filepath.Join(root, tc.target))
				} else {
					result, err = RescanOne(s, lib, filepath.Join(root, tc.target))
				}
				if err != nil {
					t.Fatal(err)
				}
				wantCount := len(tc.files)
				if tc.stack {
					wantCount = 1
				}
				if result.Success != wantCount || result.Pending != 0 || result.Skipped != 0 || result.Failed != 0 {
					t.Fatalf("result = %+v, want %d successful", result, wantCount)
				}
				if mode == "library" {
					// All fixtures form one group, including the no-CD1 fallback.
					if len(progress) != 2 || progress[0].Done != 0 || progress[1].Done != 1 || progress[1].Total != 1 || progress[1].Result.Success != wantCount {
						t.Fatalf("progress = %+v", progress)
					}
					if _, err := s.MovieIDByPath(stalePath); !store.NotFound(err) {
						t.Fatalf("library scan did not reconcile stale source: %v", err)
					}
				} else {
					if _, err := s.MovieIDByPath(stalePath); err != nil {
						t.Fatalf("single-file scan removed unrelated source: %v", err)
					}
					if len(result.IndexedSources) != wantCount {
						t.Fatalf("indexed sources = %v, want %d", result.IndexedSources, wantCount)
					}
				}
				movies := visible(t, s)
				if mode != "library" {
					wantCount++ // Unrelated missing source must remain indexed.
				}
				if len(movies) != wantCount {
					t.Fatalf("movie count = %d, want %d", len(movies), wantCount)
				}
				for _, movie := range movies {
					if movie.SourcePath == stalePath {
						continue
					}
					if tc.stack {
						wantParts := []string{filepath.Join(root, "Movie-CD2.strm"), filepath.Join(root, "Movie-CD3.strm")}
						if filepath.Base(movie.SourcePath) != "Movie-CD1.strm" || !reflect.DeepEqual(movie.AdditionalParts, wantParts) {
							t.Fatalf("stack = %s %v, want CD1 + %v", movie.SourcePath, movie.AdditionalParts, wantParts)
						}
					} else if len(movie.AdditionalParts) != 0 {
						t.Fatalf("unstacked movie has parts: %v", movie.AdditionalParts)
					}
				}
			})
		}
	}
}
