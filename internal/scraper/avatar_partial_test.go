package scraper

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/avatar"
	"emby-go/internal/store"
)

func TestAvatarSavedOutputReportsNFOAndIndexFailures(t *testing.T) {
	for _, mode := range []string{"success", "nfo", "index", "mixed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "db.sqlite")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			lib, err := db.AddLibrary("L", root)
			if err != nil {
				t.Fatal(err)
			}
			const actor = "Fixture actor"
			raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			wantNFO, wantIndex := 0, 0
			for n := range 3 {
				source := filepath.Join(root, fmt.Sprintf("ABC-%03d.strm", 100+n))
				nfoPath := strings.TrimSuffix(source, ".strm") + ".nfo"
				if err = os.WriteFile(source, []byte("https://fixture.invalid/movie.mp4"), 0o600); err != nil {
					t.Fatal(err)
				}
				nfoFailure := n == 0 && (mode == "nfo" || mode == "mixed" || mode == "canceled")
				if nfoFailure {
					if err = os.Mkdir(nfoPath, 0o700); err != nil {
						t.Fatal(err)
					}
					wantNFO++
				} else {
					if err = os.WriteFile(nfoPath, []byte("<movie><title>from NFO</title><actor><name>"+actor+"</name></actor></movie>"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				id, e := db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: source, NFOPath: nfoPath, Title: "before", Status: "success"}, 1, time.Unix(1, 0), []store.ActorRef{{Name: actor}})
				if e != nil {
					t.Fatal(e)
				}
				if n == 1 && (mode == "index" || mode == "mixed") {
					_, err = raw.Exec(fmt.Sprintf("CREATE TRIGGER fail_avatar_rescan BEFORE UPDATE ON movies WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'private-avatar-sql'); END", id))
					if err != nil {
						t.Fatal(err)
					}
					wantIndex++
				}
			}
			picture := avatarPNG(t, 1)
			var backend *httptest.Server
			backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/picture.png" {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(picture)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "one", "name": actor, "provider": "fixture", "images": []string{backend.URL + "/picture.png"}}}})
			}))
			defer backend.Close()
			dir := filepath.Join(root, "avatars")
			s := New(Config{BaseURL: backend.URL, Token: "fixture", Concurrency: 1, AvatarsDir: dir}, db)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var result AvatarResult
			progress, err := s.RunAvatarsFor(ctx, []string{actor}, nil, func(r AvatarResult) {
				result = r
				if mode == "canceled" {
					cancel()
				}
			})
			partial := wantNFO+wantIndex > 0
			if errors.Is(err, ErrAvatarWriteIncomplete) != partial || errors.Is(err, context.Canceled) != (mode == "canceled") {
				t.Fatalf("result error=%v", err)
			}
			if result.NFOFailed != wantNFO || result.IndexRefreshFailed != wantIndex || result.Movies != 3-wantNFO-wantIndex || result.Fatal || progress.Consecutive != 0 {
				t.Fatalf("item=%+v progress=%+v", result, progress)
			}
			if partial {
				if result.Status != StatusFailed || progress.Failed != 1 || progress.Success != 0 || result.Reason != ErrAvatarWriteIncomplete.Error() {
					t.Fatalf("partial reported as success: %+v %+v", result, progress)
				}
			} else if result.Status != StatusSuccess || progress.Success != 1 || progress.Failed != 0 {
				t.Fatalf("success %+v %+v", result, progress)
			}
			if partial && (strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private-avatar-sql")) {
				t.Fatalf("private failure leaked: %v", err)
			}
			image, err := os.ReadFile(avatar.Path(dir, actor))
			if err != nil || len(image) == 0 {
				t.Fatalf("saved avatar lost: %v", err)
			}
			indexed, err := db.ActorAvatar(actor)
			if err != nil || indexed.AvatarTag == "" {
				t.Fatalf("final actor index not attempted: %+v %v", indexed, err)
			}
			movies, err := db.MoviesByActor(actor)
			if err != nil || len(movies) != 3 {
				t.Fatalf("movies %+v %v", movies, err)
			}
			// A healthy movie after both faulty ones must still be indexed.
			if movies[2].Title != "from NFO" {
				t.Fatalf("healthy later movie not processed: %+v", movies[2])
			}
			if wantIndex > 0 {
				if movies[1].Title != "before" {
					t.Fatal("failed index changed metadata")
				}
				b, e := os.ReadFile(movies[1].NFOPath)
				if e != nil || !strings.Contains(string(b), backend.URL+"/picture.png") {
					t.Fatalf("saved NFO lost: %s %v", b, e)
				}
			}
		})
	}
}
