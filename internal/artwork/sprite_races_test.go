package artwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSpriteRootAndNamespaceSubstitution(t *testing.T) {
	for _, which := range []string{"root", "movie"} {
		t.Run(which, func(t *testing.T) {
			db, m, dir := fixture(t)
			opts := spriteTestOptions(t, dir, "1")
			original, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID}, opts)
			if err != nil {
				t.Fatal(err)
			}
			victim := opts.RootDir
			if which == "movie" {
				victim = filepath.Join(victim, fmt.Sprint(m.ID))
			}
			moved := victim + "-moved"
			called := false
			opts.Progress = func(done, total int) {
				if called {
					return
				}
				called = true
				if e := os.Rename(victim, moved); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(victim, 0700); e != nil {
					t.Fatal(e)
				}
			}
			res, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID, Overwrite: true}, opts)
			if err == nil || res.Changed {
				t.Fatalf("substituted %s published: %+v %v", which, res, err)
			}
			state, e := db.Sprite(m.ID)
			if e != nil || state.Generation != original.Manifest.Generation {
				t.Fatal("old pointer changed", e)
			}
			parent := moved
			if which == "root" {
				parent = filepath.Join(moved, fmt.Sprint(m.ID))
			}
			entries, e := os.ReadDir(parent)
			if e != nil || len(entries) != 1 || entries[0].Name() != original.Manifest.Generation {
				t.Fatalf("owned unpublished generation leaked or old generation removed: %v %v", entries, e)
			}
		})
	}
}
func TestSpriteSourceSymlinkReader(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "1")
	res, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID}, opts)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.strm")
	os.WriteFile(outside, []byte("https://trusted.example/video"), 0600)
	if err = os.Remove(m.SourcePath); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, m.SourcePath); err != nil {
		t.Skip(err)
	}
	if _, err = ReadSpriteSheet(context.Background(), db, m.ID, res.Manifest.Generation, 0, opts.RootDir); err == nil || errors.Is(err, ErrSpriteNotFound) {
		t.Fatalf("source symlink not refused: %v", err)
	}
}
