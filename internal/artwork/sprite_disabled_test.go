package artwork

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSpriteDisabledLibraryGenerationAndReads(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "1")
	ctx := context.Background()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	setEnabled := func(value int) {
		t.Helper()
		if _, err := raw.Exec("UPDATE libraries SET enabled=? WHERE id=?", value, m.LibraryID); err != nil {
			t.Fatal(err)
		}
	}
	setEnabled(0)
	if _, err = GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID}, opts); err == nil {
		t.Fatal("generated for disabled library")
	}
	if _, err = os.Stat(opts.RootDir); !os.IsNotExist(err) {
		t.Fatal("disabled generation created root")
	}
	setEnabled(1)
	res, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID}, opts)
	if err != nil {
		t.Fatal(err)
	}
	setEnabled(0)
	if _, err = GetSpriteManifest(ctx, db, m.ID, opts.RootDir); err == nil {
		t.Fatal("read manifest for disabled library")
	}
	if _, err = ReadSpriteSheet(ctx, db, m.ID, res.Manifest.Generation, 0, opts.RootDir); err == nil {
		t.Fatal("read sheet for disabled library")
	}
	if _, err = ReadSpriteBIF(ctx, db, m.ID, res.Manifest.Generation, opts.RootDir); err == nil {
		t.Fatal("read BIF for disabled library")
	}
	if _, err = ReadSpriteFrame(ctx, db, m.ID, res.Manifest.Generation, 0, opts.RootDir); err == nil {
		t.Fatal("read frame for disabled library")
	}
	setEnabled(1)
	opts.Progress = func(done, total int) { setEnabled(0) }
	if out, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID, Overwrite: true}, opts); err == nil || out.Changed {
		t.Fatalf("published after library disabled: %+v %v", out, err)
	}
	state, err := db.Sprite(m.ID)
	if err != nil || state.Generation != res.Manifest.Generation {
		t.Fatal("disabled CAS changed old pointer", err)
	}
	entries, err := os.ReadDir(filepath.Join(opts.RootDir, spriteMovieDir(m.ID)))
	if err != nil || len(entries) != 1 {
		t.Fatalf("disabled CAS leaked files %v %v", entries, err)
	}
}
