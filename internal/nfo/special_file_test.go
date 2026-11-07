package nfo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNFOOrdinaryFileSymlinkCompatibility(t *testing.T) {
	for _, operation := range []string{"UpdateScraped", "UpdateScrapedWithAutoMarks", "SaveFileInfo", "CommitTranslation"} {
		t.Run(operation, func(t *testing.T) {
			// The existing reader follows ordinary file links, including targets
			// outside the sidecar directory. Writers replace the link, not its target.
			original := []byte("<movie><title>old</title><originaltitle>source</originaltitle><tag>C</tag></movie>")
			target := filepath.Join(t.TempDir(), "outside.nfo")
			if err := os.WriteFile(target, original, 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "movie.nfo")
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("file symlink unavailable: %v", err)
			}
			if got, err := ReadFile(path); err != nil || !slices.Equal(got, original) {
				t.Fatalf("linked bytes: %q, %v", got, err)
			}
			if got, err := Read(path); err != nil || got.Title != "old" {
				t.Fatalf("linked metadata: %+v, %v", got, err)
			}
			var err error
			switch operation {
			case "UpdateScraped":
				err = UpdateScraped(path, ScrapeFields{Overwrite: true, Title: "updated"})
			case "UpdateScrapedWithAutoMarks":
				var owned []string
				owned, err = UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: true, Title: "updated"}, []string{"C"}, []string{"UC"})
				if err == nil && !slices.Equal(owned, []string{"UC"}) {
					t.Fatalf("ownership: %v", owned)
				}
			case "SaveFileInfo":
				err = SaveFileInfo(path, FileInfoMeta{Size: 123}, &StreamDetails{})
			case "CommitTranslation":
				snap, readErr := ReadTranslationSnapshot(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				var changed bool
				changed, err = CommitTranslation(context.Background(), path, snap, "updated", "")
				if err == nil && !changed {
					t.Fatal("translation did not change linked NFO")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("updated path is not an ordinary file: %v, %v", info, err)
			}
			got, err := os.ReadFile(target)
			if err != nil || !slices.Equal(got, original) {
				t.Fatalf("write followed link into target: %q, %v", got, err)
			}
			meta, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "SaveFileInfo" {
				if meta.FileInfo == nil || meta.FileInfo.Size != 123 || meta.Title != "old" {
					t.Fatalf("fileinfo update: %+v", meta)
				}
			} else if meta.Title != "updated" {
				t.Fatalf("updated title: %q", meta.Title)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("unexpected temporary files: %v, %v", entries, err)
			}
		})
	}
}
