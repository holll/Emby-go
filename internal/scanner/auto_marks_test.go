package scanner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestManualScanPreservesAutoMarks(t *testing.T) {
	for _, mode := range []string{"full", "incremental", "rescan", "reindex"} {
		t.Run(mode, func(t *testing.T) {
			root, s, lib := scanFixture(t)
			source := filepath.Join(root, "A.strm")
			nfoPath := filepath.Join(root, "A.nfo")
			id, err := s.MovieIDByPath(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetAutoMarks(id, []string{"中文字幕"}); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkManual(source, nfoPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(nfoPath); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "full":
				_, err = Scan(s, lib)
			case "incremental":
				_, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
			case "rescan":
				_, err = RescanOne(s, lib, source)
			case "reindex":
				_, err = ReindexOne(s, lib, source)
			}
			if err != nil {
				t.Fatal(err)
			}
			movie, err := s.Movie(id)
			if err != nil || movie.Status != "manual" || !slices.Equal(movie.AutoMarks, []string{"中文字幕"}) {
				t.Fatalf("manual scan changed provenance: %+v err=%v", movie, err)
			}
		})
	}
}

func TestScanPreservesAutoMarksWithoutInferringOwnership(t *testing.T) {
	for _, mode := range []string{"full", "incremental", "rescan", "reindex"} {
		t.Run(mode, func(t *testing.T) {
			root, s, lib := scanFixture(t)
			source := filepath.Join(root, "A.strm")
			id, err := s.MovieIDByPath(source)
			if err != nil {
				t.Fatal(err)
			}
			scan := func() {
				t.Helper()
				var err error
				switch mode {
				case "full":
					_, err = Scan(s, lib)
				case "incremental":
					_, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
				case "rescan":
					_, err = RescanOne(s, lib, source)
				case "reindex":
					_, err = ReindexOne(s, lib, source)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, tags := range []string{
				"<movie><title>Changed title</title><tag>中文字幕</tag><tag>Custom</tag></movie>",
				"<movie><title>Changed again</title><tag>Custom</tag></movie>",
			} {
				if err := s.SetAutoMarks(id, []string{"中文字幕"}); err != nil {
					t.Fatal(err)
				}
				scanWrite(t, filepath.Join(root, "A.nfo"), tags)
				scan()
				movie, err := s.Movie(id)
				if err != nil || !slices.Equal(movie.AutoMarks, []string{"中文字幕"}) {
					t.Fatalf("scan changed provenance: %+v err=%v", movie, err)
				}
			}
			// A missing record is not reconstructed from an NFO label, even if it
			// happens to equal one of the program's generated tag texts.
			if err := s.SetAutoMarks(id, nil); err != nil {
				t.Fatal(err)
			}
			scanWrite(t, filepath.Join(root, "A.nfo"), "<movie><title>Ownership unknown</title><tag>中文字幕</tag></movie>")
			scan()
			movie, err := s.Movie(id)
			if err != nil || len(movie.AutoMarks) != 0 || !slices.Contains(movie.Tags, "中文字幕") {
				t.Fatalf("scan inferred ownership or lost tag: %+v err=%v", movie, err)
			}
			// Rebuilding a new DB cannot reconstruct provenance either.
			rebuilt := newStore(t, t.TempDir())
			rebuiltLib, err := rebuilt.AddLibrary("rebuilt", root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Scan(rebuilt, rebuiltLib); err != nil {
				t.Fatal(err)
			}
			rebuiltID, err := rebuilt.MovieIDByPath(source)
			if err != nil {
				t.Fatal(err)
			}
			rebuiltMovie, err := rebuilt.Movie(rebuiltID)
			if err != nil || len(rebuiltMovie.AutoMarks) != 0 || !slices.Contains(rebuiltMovie.Tags, "中文字幕") {
				t.Fatalf("rebuild inferred ownership or lost tag: %+v err=%v", rebuiltMovie, err)
			}
		})
	}
}
