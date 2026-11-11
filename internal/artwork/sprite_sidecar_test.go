package artwork

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSpriteSidecarNFOPathChangeRejectsOldManifest(t *testing.T) {
	db, movie, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "2.5")
	first, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, IntervalMs: 1000}, opts)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(dir, ".A-"+first.Manifest.Generation+".bif"))
	if err != nil {
		t.Fatal(err)
	}
	movie.NFOPath = filepath.Join(dir, "renamed.nfo")
	if _, err := db.IndexMovie(movie, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	// Even a byte-identical backing at the new derived path must not make an
	// old manifest appear bound to the new indexed NFO.
	if err := os.WriteFile(filepath.Join(dir, ".renamed-"+first.Manifest.Generation+".bif"), original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"", first.Manifest.Generation} {
		if _, err := ReadSpriteBIF(context.Background(), db, movie.ID, generation, opts.RootDir); err == nil {
			t.Fatalf("old generation accepted after NFO path changed: %q", generation)
		}
	}
}

func TestSpriteSidecarForeignCanonicalRequiresExplicitOverwrite(t *testing.T) {
	db, movie, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "2.5")
	canonical := filepath.Join(dir, "A.bif")
	foreign := []byte("user-owned independent BIF")
	if err := os.WriteFile(canonical, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	before, beforeErr := db.SpriteContext(context.Background(), movie.ID)
	if beforeErr != nil && !errors.Is(beforeErr, sql.ErrNoRows) {
		t.Fatal(beforeErr)
	}
	result, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, IntervalMs: 1000}, opts)
	if err == nil || result.Changed {
		t.Fatalf("foreign sidecar overwritten without permission: %+v, %v", result, err)
	}
	data, err := os.ReadFile(canonical)
	if err != nil || !bytes.Equal(data, foreign) {
		t.Fatalf("foreign canonical changed on conflict: %q, %v", data, err)
	}
	after, afterErr := db.SpriteContext(context.Background(), movie.ID)
	if !reflect.DeepEqual(before, after) || !errors.Is(afterErr, beforeErr) {
		t.Fatalf("conflict changed published DB state: before %+v/%v, after %+v/%v", before, beforeErr, after, afterErr)
	}
	result, err = GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, Overwrite: true, IntervalMs: 1000}, opts)
	if err != nil || !result.Changed {
		t.Fatalf("explicit overwrite failed: %+v, %v", result, err)
	}
	data, err = os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	served, err := ReadSpriteBIF(context.Background(), db, movie.ID, result.Manifest.Generation, opts.RootDir)
	if err != nil || !bytes.Equal(data, served) || bytes.Equal(data, foreign) {
		t.Fatalf("explicit overwrite did not publish matching canonical: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bif-alias-") {
			t.Fatalf("sidecar publication leaked temporary file: %s", entry.Name())
		}
	}
}

func TestSpriteSidecarCancellationBeforeCASPreservesOldGeneration(t *testing.T) {
	db, movie, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "2.5")
	first, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, IntervalMs: 1000}, opts)
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(dir, "A.bif")
	original, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.SpriteContext(context.Background(), movie.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepared := false
	phaseHook = func(phase string) {
		if phase == "prepare-done" {
			prepared = true
			cancel()
		}
	}
	defer func() { phaseHook = nil }()
	result, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: movie.ID, Overwrite: true, IntervalMs: 500}, opts)
	if !prepared || !errors.Is(err, context.Canceled) || result.Changed {
		t.Fatalf("CAS cancellation failed: prepared=%v result=%+v err=%v", prepared, result, err)
	}
	after, err := db.SpriteContext(context.Background(), movie.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("cancellation changed DB state: before %+v, after %+v, err %v", before, after, err)
	}
	data, err := os.ReadFile(canonical)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("cancellation changed canonical: %v", err)
	}
	served, err := ReadSpriteBIF(context.Background(), db, movie.ID, first.Manifest.Generation, opts.RootDir)
	if err != nil || !bytes.Equal(served, original) {
		t.Fatalf("old generation unreadable after cancellation: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bif-alias-") || strings.HasSuffix(entry.Name(), ".bif.tmp") {
			t.Fatalf("cancelled publication leaked temporary file: %s", entry.Name())
		}
	}
}
