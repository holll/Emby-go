package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/store"
)

type adminDeleteFixture struct {
	a                            *App
	db                           *sql.DB
	lib, other, victim, survivor int64
	files                        map[string]string
}

func newAdminDeleteFixture(t *testing.T) adminDeleteFixture {
	t.Helper()
	a, _, path := authorityApp(t)
	a.startupWG.Wait()
	fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.Close() })
	f := adminDeleteFixture{a: a, db: fixture, files: make(map[string]string)}
	root := t.TempDir()
	for _, target := range []bool{true, false} {
		dir := filepath.Join(root, fmt.Sprint(target))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		lib, err := a.db.AddLibrary(fmt.Sprint(target), dir)
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"movie.strm": "https://media.test/fixture.mp4", "movie.nfo": "<movie><title>Keep files</title></movie>"} {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			f.files[path] = body
		}
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(dir, "movie.strm"), NFOPath: filepath.Join(dir, "movie.nfo"), Title: fmt.Sprint(target), Status: "success", Genres: []string{"Drama"}}, 1, time.Unix(1, 0), []store.ActorRef{{Name: "Actor"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.db.SavePlayback(id, 120, 3, "fixture", 99); err != nil {
			t.Fatal(err)
		}
		if target {
			f.lib, f.victim = lib.ID, id
		} else {
			f.other, f.survivor = lib.ID, id
		}
	}
	if err := a.db.SaveAccessToken("fixture-admin-delete"); err != nil {
		t.Fatal(err)
	}
	return f
}

func adminDeleteSnapshot(t *testing.T, f adminDeleteFixture) string {
	t.Helper()
	var snapshot []any
	for _, q := range []string{"SELECT * FROM libraries ORDER BY id", "SELECT * FROM movies ORDER BY id", "SELECT * FROM actors ORDER BY name", "SELECT * FROM movie_actors ORDER BY movie_id,actor_name", "SELECT * FROM movie_features ORDER BY movie_id,kind,value", "SELECT * FROM userdata ORDER BY movie_id", "SELECT key,value FROM kv WHERE key='g:version' OR key LIKE 'lib:%:version' ORDER BY key"} {
		rows, err := f.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		snapshot = append(snapshot, q)
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot = append(snapshot, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot = append(snapshot, f.a.db.Version("g:version"))
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func adminDeleteRequest(f adminDeleteFixture, kind, token string) *httptest.ResponseRecorder {
	id := f.victim
	if kind == "libraries" {
		id = f.lib
	}
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/%s/%d", kind, id), nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, req)
	return w
}

func TestAdminDeleteAtomicFailureAndRetry(t *testing.T) {
	for _, kind := range []string{"items", "libraries"} {
		for _, fault := range []string{"global", "library", "delete", "commit"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := newAdminDeleteFixture(t)
				before := adminDeleteSnapshot(t, f)
				f.a.cache.Set("fixture:delete-marker", []byte("retain-until-commit"), time.Hour)
				for _, token := range []string{"", "unknown"} {
					if w := adminDeleteRequest(f, kind, token); w.Code != http.StatusUnauthorized {
						t.Fatalf("unauthorized status=%d", w.Code)
					}
				}
				if adminDeleteSnapshot(t, f) != before {
					t.Fatal("unauthorized deletion changed business state")
				}
				var trigger string
				switch fault {
				case "global":
					trigger = `CREATE TRIGGER fail_admin_delete BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'fixture-version'); END`
				case "library":
					trigger = fmt.Sprintf(`CREATE TRIGGER fail_admin_delete BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'fixture-library-version'); END`, f.lib)
				case "delete":
					trigger = `CREATE TRIGGER fail_admin_delete BEFORE DELETE ON movies BEGIN SELECT RAISE(ABORT,'fixture-delete'); END`
				case "commit":
					trigger = `CREATE TABLE fixture_admin_parent(id INTEGER PRIMARY KEY); CREATE TABLE fixture_admin_child(parent_id INTEGER REFERENCES fixture_admin_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_admin_delete AFTER DELETE ON movies BEGIN INSERT INTO fixture_admin_child(parent_id) VALUES(999); END`
				}
				if _, err := f.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
				if w := adminDeleteRequest(f, kind, "fixture-admin-delete"); w.Code != http.StatusInternalServerError {
					t.Fatalf("failed deletion status=%d body=%s", w.Code, w.Body.String())
				}
				if adminDeleteSnapshot(t, f) != before {
					t.Fatal("failed API deletion changed rows or versions")
				}
				if _, ok := f.a.cache.Get("fixture:delete-marker"); !ok {
					t.Fatal("failed transaction cleared cache")
				}
				if _, err := f.db.Exec("DROP TRIGGER fail_admin_delete"); err != nil {
					t.Fatal(err)
				}
				if w := adminDeleteRequest(f, kind, "fixture-admin-delete"); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
					t.Fatalf("retry status=%d body=%s", w.Code, w.Body.String())
				}
				if _, ok := f.a.cache.Get("fixture:delete-marker"); ok {
					t.Fatal("successful deletion did not clear cache")
				}
				if _, err := f.a.db.Movie(f.victim); !store.NotFound(err) {
					t.Fatalf("victim still exists: %v", err)
				}
				if _, err := f.a.db.Movie(f.survivor); err != nil {
					t.Fatalf("other library lost movie: %v", err)
				}
				if _, err := f.a.db.Library(f.other); err != nil {
					t.Fatalf("other library lost: %v", err)
				}
				_, libErr := f.a.db.Library(f.lib)
				if kind == "libraries" && !store.NotFound(libErr) || kind == "items" && libErr != nil {
					t.Fatalf("library state after %s: %v", kind, libErr)
				}
				for key, want := range map[string]string{"g:version": "3", fmt.Sprintf("lib:%d:version", f.lib): "2", fmt.Sprintf("lib:%d:version", f.other): "1"} {
					var got string
					if err := f.db.QueryRow("SELECT value FROM kv WHERE key=?", key).Scan(&got); err != nil || got != want {
						t.Fatalf("%s=%s want=%s err=%v", key, got, want, err)
					}
				}
				if f.a.db.Version("g:version") != "3" {
					t.Fatal("successful API deletion did not publish exact version")
				}
				for path, want := range f.files {
					body, err := os.ReadFile(path)
					if err != nil || string(body) != want {
						t.Fatalf("delete touched disk %s: %q %v", path, body, err)
					}
				}
				after := adminDeleteSnapshot(t, f)
				f.a.cache.Set("fixture:delete-marker", []byte("missing-must-not-clear"), time.Hour)
				if w := adminDeleteRequest(f, kind, "fixture-admin-delete"); w.Code != http.StatusNotFound {
					t.Fatalf("repeat status=%d", w.Code)
				}
				if adminDeleteSnapshot(t, f) != after {
					t.Fatal("missing delete changed rows or versions")
				}
				if _, ok := f.a.cache.Get("fixture:delete-marker"); !ok {
					t.Fatal("missing delete cleared cache")
				}
			})
		}
	}
}
