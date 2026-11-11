package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/artwork"
)

func TestBIFSidecarRestoredWithoutReextracting(t *testing.T) {
	f := artworkFixture(t)
	manifest := spriteSeed(t, f)
	movie, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSuffix(movie.NFOPath, filepath.Ext(movie.NFOPath)) + ".bif"
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An external player may modify the visible sidecar. It must not mutate
	// the immutable generation used by authenticated BIF requests.
	if err := os.WriteFile(path, []byte("external change"), 0600); err != nil {
		t.Fatal(err)
	}
	response := translationCall(f.a, f.token, http.MethodGet, fmt.Sprintf("/Videos/%d/index.bif", f.movie), "")
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), original) {
		t.Fatalf("external sidecar change affected published generation: %d", response.Code)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	opts, err := f.a.spriteOptions()
	if err != nil {
		t.Fatal(err)
	}
	opts.FFmpegPath, opts.FFprobePath = "/does-not-exist", "/does-not-exist"
	result, err := artwork.GenerateSprite(context.Background(), f.a.db, artwork.SpriteRequest{MovieID: f.movie}, opts)
	if err != nil || !result.Skipped || result.Changed || result.Manifest.Generation != manifest.Generation {
		t.Fatalf("sidecar repair must reuse the published generation: %+v %v", result, err)
	}
	restored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatalf("non-overwrite task did not restore the same-name BIF: %v", err)
	}
}
