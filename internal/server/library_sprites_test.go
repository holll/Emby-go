package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/store"
)

func TestLibrarySpritesSettingsPermissionsAndValidation(t *testing.T) {
	f := artworkFixture(t)
	path := fmt.Sprintf("/api/admin/libraries/%d/sprites", f.lib)
	user, err := f.a.db.CreateUser("sprite-library-user", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("sprite-library-user", user.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]int{"": 401, "sprite-library-user": 403} {
		if w := translationCall(f.a, token, "PUT", path, `{"interval_seconds":60}`); w.Code != want {
			t.Fatalf("permission %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"interval_seconds":null}`, `{"interval_seconds":9}`, `{"interval_seconds":86401}`, `{"interval_seconds":1.5}`, `{"interval_seconds":"60"}`, `{"Interval_seconds":60}`, `{"interval_seconds":60,"interval_seconds":300}`, `{"interval_seconds":60,"path":"x"}`} {
		if w := translationCall(f.a, f.token, "PUT", path, body); w.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, n := range []int{300, 60, 10, 17} {
		w := translationCall(f.a, f.token, "PUT", path, fmt.Sprintf(`{"interval_seconds":%d}`, n))
		if w.Code != 200 {
			t.Fatalf("save %d %s", w.Code, w.Body.String())
		}
		c := translationCall(f.a, f.token, "GET", fmt.Sprintf("/api/admin/sprites/capabilities?movie_id=%d", f.movie), "")
		var body struct {
			Interval int `json:"interval_seconds"`
		}
		if c.Code != 200 || json.Unmarshal(c.Body.Bytes(), &body) != nil || body.Interval != n {
			t.Fatalf("capabilities %d %s", c.Code, c.Body.String())
		}
	}
	w := translationCall(f.a, f.token, "GET", "/api/admin/libraries", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sprite_interval_seconds":17`) {
		t.Fatalf("listing %d %s", w.Code, w.Body.String())
	}
	if w := translationCall(f.a, f.token, "PUT", "/api/admin/libraries/999999/sprites", `{"interval_seconds":60}`); w.Code != 404 {
		t.Fatalf("missing %d", w.Code)
	}
	// A revoked actor may not save after a slow request body has been consumed.
	raw := `{"interval_seconds":60}`
	body := &passwordIdentityReader{Reader: strings.NewReader(raw), beforeRead: func() {
		if err := f.a.db.DeleteAccessToken(f.token); err != nil {
			t.Fatal(err)
		}
	}}
	req := httptest.NewRequest("PUT", path, body)
	req.ContentLength = int64(len(raw))
	req.Header.Set("X-Emby-Token", f.token)
	out := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatalf("revoked %d", out.Code)
	}
}

func TestLibrarySpritesBatchInheritsEachLibraryAndPreservesPreview(t *testing.T) {
	f := artworkFixture(t)
	prior := spriteSeed(t, f)
	other, err := f.a.db.AddLibrary("second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	movie, err := f.a.db.IndexMovie(store.Movie{LibraryID: other.ID, SourcePath: filepath.Join(other.Path, "other.strm"), Status: "success"}, 1, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for lib, n := range map[int64]int{f.lib: 300, other.ID: 60} {
		if err := f.a.db.SetLibrarySpriteInterval(context.Background(), lib, n); err != nil {
			t.Fatal(err)
		}
	}
	// Saving density does not touch the published preview pointer.
	state, err := f.a.db.Sprite(f.movie)
	if err != nil || state.Generation != prior.Generation {
		t.Fatalf("preview changed %v", err)
	}
	got := map[int64]int64{}
	f.a.sprites.run = func(_ context.Context, _ *store.Store, req artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		got[req.MovieID] = req.IntervalMs
		return artwork.SpriteResult{Skipped: true}, nil
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", fmt.Sprintf(`{"movie_ids":[%d,%d]}`, f.movie, movie))
	if w.Code != 202 {
		t.Fatalf("run %d %s", w.Code, w.Body.String())
	}
	spriteJoin(t, f.a)
	if got[f.movie] != 300000 || got[movie] != 60000 {
		t.Fatalf("per-library inheritance %v", got)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", fmt.Sprintf(`{"movie_ids":[%d],"interval_seconds":10}`, f.movie)); w.Code != 400 {
		t.Fatalf("per-film override accepted: %d", w.Code)
	}
}
