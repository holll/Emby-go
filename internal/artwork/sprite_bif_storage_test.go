package artwork

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpriteBIFUsesIndexedNFODirectoryAndRetainsGenerations(t *testing.T) {
	db, movie, dir := fixture(t)
	nfoDir := filepath.Join(dir, "metadata with spaces")
	if err := os.Mkdir(nfoDir, 0700); err != nil {
		t.Fatal(err)
	}
	movie.NFOPath = filepath.Join(nfoDir, "真实 NFO.nfo")
	if err := os.WriteFile(movie.NFOPath, []byte("<movie/>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.IndexMovie(movie, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	opts := spriteTestOptions(t, dir, "2.5")
	var generations []string
	for i := 0; i < 4; i++ {
		result, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, Overwrite: i > 0, IntervalMs: 1000}, opts)
		if err != nil || !result.Changed {
			t.Fatalf("generation %d: %+v %v", i, result, err)
		}
		gen := result.Manifest.Generation
		generations = append(generations, gen)
		path := filepath.Join(nfoDir, ".真实 NFO-"+gen+".bif")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = validateSpriteBIF(data, result.Manifest); err != nil {
			t.Fatal(err)
		}
		canonical, err := os.ReadFile(filepath.Join(nfoDir, "真实 NFO.bif"))
		if err != nil || !bytes.Equal(canonical, data) {
			t.Fatalf("same-stem canonical differs from active generation: %v", err)
		}
		legacy := filepath.Join(opts.RootDir, spriteGenerationDir(movie.ID, gen), "index.bif")
		if _, err = os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("new generation retained legacy BIF: %v", err)
		}
		served, err := ReadSpriteBIF(context.Background(), db, movie.ID, gen, opts.RootDir)
		if err != nil || !bytes.Equal(served, data) {
			t.Fatalf("read BIF: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(nfoDir, ".真实 NFO-"+generations[0]+".bif")); !os.IsNotExist(err) {
		t.Fatalf("pruned generation BIF retained: %v", err)
	}
	for _, gen := range generations[1:] {
		if _, err := ReadSpriteBIF(context.Background(), db, movie.ID, gen, opts.RootDir); err != nil {
			t.Fatalf("retained generation %s unreadable: %v", gen, err)
		}
	}
}

func TestSpriteLegacyBIFMigratesOnSkipWithoutExtractingFrames(t *testing.T) {
	db, movie, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "2.5")
	first, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, IntervalMs: 1000}, opts)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".A-"+first.Manifest.Generation+".bif")
	genDir := filepath.Join(opts.RootDir, spriteGenerationDir(movie.ID, first.Manifest.Generation))
	legacy := filepath.Join(genDir, "index.bif")
	// Reconstruct the pre-upgrade layout, including its unbound manifest.
	manifestPath := filepath.Join(genDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]json.RawMessage
	if err = json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	delete(disk, "nfo_path")
	raw, err = json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, legacy); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(dir, "A.bif")
	if err = os.Remove(canonical); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSpriteBIF(context.Background(), db, movie.ID, "", opts.RootDir)
	if err != nil {
		t.Fatalf("legacy read: %v", err)
	}
	opts.FFmpegPath = filepath.Join(dir, "must-not-run-ffmpeg")
	opts.FFprobePath = filepath.Join(dir, "must-not-run-ffprobe")
	result, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID}, opts)
	if err != nil || !result.Skipped || result.Changed || result.Manifest.Generation != first.Manifest.Generation {
		t.Fatalf("migration re-extracted or changed generation: %+v %v", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("migrated bytes differ: %v", err)
	}
	alias, err := os.ReadFile(canonical)
	if err != nil || !bytes.Equal(alias, before) {
		t.Fatalf("legacy migration did not publish same-stem canonical: %v", err)
	}
	if _, err = os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy BIF retained: %v", err)
	}
	if _, err = ReadSpriteBIF(context.Background(), db, movie.ID, "", opts.RootDir); err != nil {
		t.Fatalf("migrated read: %v", err)
	}
	// A second no-op uses the adjacent location, not a deleted legacy asset.
	if result, err = GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID}, opts); err != nil || !result.Skipped {
		t.Fatalf("second skip: %+v %v", result, err)
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadSpriteBIF(context.Background(), db, movie.ID, "", opts.RootDir); err == nil {
		t.Fatal("corrupt adjacent BIF read")
	}
	if _, err = GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID}, opts); err == nil {
		t.Fatal("corrupt adjacent BIF silently regenerated")
	}
}

func TestSpriteBIFRejectsMissingOutsideAndSymlinkNFOPaths(t *testing.T) {
	for _, kind := range []string{"missing", "outside", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			db, movie, dir := fixture(t)
			switch kind {
			case "missing":
				movie.NFOPath = ""
			case "outside":
				movie.NFOPath = filepath.Join(t.TempDir(), "outside.nfo")
			case "symlink":
				outside := t.TempDir()
				linked := filepath.Join(dir, "linked")
				if err := os.Symlink(outside, linked); err != nil {
					t.Skip(err)
				}
				movie.NFOPath = filepath.Join(linked, "A.nfo")
			}
			if _, err := db.IndexMovie(movie, 1, time.Now(), nil); err != nil {
				t.Fatal(err)
			}
			_, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, IntervalMs: 1000}, spriteTestOptions(t, dir, "1"))
			if err == nil {
				t.Fatal("unsafe NFO accepted")
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".bif") || strings.HasSuffix(entry.Name(), ".bif.tmp") {
					t.Fatalf("failed generation left BIF: %s", entry.Name())
				}
			}
		})
	}
}
