package scraper

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/avatar"
	"emby-go/internal/store"
)

func TestAvatarTaskUsesAtomicVersionedActorIndex(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(strconv.FormatBool(fault), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			lib, err := db.AddLibrary("L", filepath.Join(root, "media"))
			if err != nil {
				t.Fatal(err)
			}
			const name = "Fixture actor"
			// No NFO in this fixture: isolate the final avatar-index write from the
			// per-movie NFO/rescan stage, which has its own versions and failure policy.
			id, err := db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "movie.strm"), Status: "success"}, 1, time.Unix(1, 0), []store.ActorRef{{Name: name}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			g := db.Version("g:version")
			key := "lib:" + strconv.FormatInt(lib.ID, 10) + ":version"
			l := db.Version(key)
			raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if fault {
				if _, err = raw.Exec("CREATE TRIGGER fail_final_avatar BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'avatar-version-failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			img := image.NewRGBA(image.Rect(0, 0, 2, 2))
			img.Set(0, 0, color.RGBA{R: 255, A: 255})
			var picture bytes.Buffer
			if err = png.Encode(&picture, img); err != nil {
				t.Fatal(err)
			}
			var backend *httptest.Server
			backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/picture.png" {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(picture.Bytes())
					return
				}
				if r.URL.Path != "/v1/actors/search" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "fixture", "name": name, "provider": "local", "images": []string{backend.URL + "/picture.png"}}}})
			}))
			defer backend.Close()
			dir := filepath.Join(root, "avatars")
			s := New(Config{BaseURL: backend.URL, Token: "fixture", Concurrency: 1, AvatarsDir: dir}, db)
			var item AvatarResult
			progress, err := s.RunAvatarsFor(context.Background(), []string{name}, nil, func(result AvatarResult) { item = result })
			if err != nil {
				t.Fatal(err)
			} // Batch-level error semantics are unchanged.
			body, err := os.ReadFile(avatar.Path(dir, name))
			if err != nil || len(body) == 0 {
				t.Fatalf("local avatar not saved: %v", err)
			}
			actor, err := db.ActorAvatar(name)
			if err != nil {
				t.Fatal(err)
			}
			if fault {
				if progress.Failed != 1 || progress.Success != 0 || item.Status != StatusFailed || item.Fatal || actor.AvatarTag != "" || actor.AvatarURL != "" || db.Version("g:version") != g || db.Version(key) != l {
					t.Fatalf("fault progress=%+v actor=%+v item=%+v", progress, actor, item)
				}
				if _, err = raw.Exec("DROP TRIGGER fail_final_avatar"); err != nil {
					t.Fatal(err)
				}
				progress, err = s.RunAvatarsFor(context.Background(), []string{name}, nil, func(result AvatarResult) { item = result })
				if err != nil {
					t.Fatal(err)
				}
				actor, err = db.ActorAvatar(name)
				if err != nil {
					t.Fatal(err)
				}
			}
			if progress.Success != 1 || item.Status != StatusSuccess || actor.AvatarURL != backend.URL+"/picture.png" || actor.AvatarTag != avatar.Tag(picture.Bytes()) {
				t.Fatalf("result %+v actor %+v", progress, actor)
			}
			gn, _ := strconv.Atoi(g)
			ln, _ := strconv.Atoi(l)
			if db.Version("g:version") != strconv.Itoa(gn+1) || db.Version(key) != strconv.Itoa(ln+1) {
				t.Fatalf("versions global=%s library=%s", db.Version("g:version"), db.Version(key))
			}
			after, err := db.Movie(id)
			if err != nil || before.Title != after.Title || before.SourcePath != after.SourcePath || before.NFOPath != after.NFOPath {
				t.Fatalf("movie changed: %+v %v", after, err)
			}
		})
	}
}
