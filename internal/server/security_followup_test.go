package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// First-read interleaving parks a real HTTP request after requireAdmin but before
// mutation, with no sleeps and no production-only synchronization hooks.
func TestAdminUserMutationRejectsInFlightRevocation(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		for _, action := range []string{"create", "update", "libraries"} {
			t.Run(action+strconv.FormatBool(deleted), func(t *testing.T) {
				a, _, _ := asyncScanFixture(t)
				actor, err := a.db.CreateUser("secondary-admin", "password-1234", true)
				if err != nil {
					t.Fatal(err)
				}
				target, err := a.db.CreateUser("target-user", "password-1234", false)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.db.SaveAccessTokenForUser("parked-session", actor.ID, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				method, path, body := "PUT", "/api/admin/users/"+strconv.FormatInt(actor.ID, 10), `{"name":"restored-admin","password":"new-password-1234","is_admin":true,"library_ids":[]}`
				switch action {
				case "create":
					method, path, body = "POST", "/api/admin/users", `{"name":"injected-admin","password":"password-1234","is_admin":true}`
				case "libraries":
					path = "/api/admin/users/" + strconv.FormatInt(target.ID, 10) + "/libraries"
					body = `{"library_ids":[]}`
				}
				ran := false
				reader := &passwordIdentityReader{Reader: strings.NewReader(body), beforeRead: func() {
					var err error
					if deleted {
						err = a.db.DeleteUser(actor.ID)
					} else {
						err = a.db.SetUserAdmin(actor.ID, false)
					}
					if err != nil {
						t.Fatal(err)
					}
					ran = true
				}}
				req := httptest.NewRequest(method, path, reader)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Emby-Token", "parked-session")
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, req)
				if !ran || w.Code != http.StatusForbidden {
					t.Fatalf("ran=%v status=%d body=%s", ran, w.Code, w.Body.String())
				}
				users, err := a.db.ListUsers()
				if err != nil {
					t.Fatal(err)
				}
				for _, u := range users {
					if u.Name == "injected-admin" || u.Name == "restored-admin" || (u.ID == actor.ID && u.IsAdmin) {
						t.Fatalf("revoked actor mutated users: %+v", u)
					}
				}
			})
		}
	}
}

func TestAdminDeleteUserRejectsStaleContext(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		a, _, _ := asyncScanFixture(t)
		actor, err := a.db.CreateUser("stale-delete-admin", "password-1234", true)
		if err != nil {
			t.Fatal(err)
		}
		victim, err := a.db.CreateUser("delete-victim", "password-1234", false)
		if err != nil {
			t.Fatal(err)
		}
		if deleted {
			err = a.db.DeleteUser(actor.ID)
		} else {
			err = a.db.SetUserAdmin(actor.ID, false)
		}
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(currentUserKey, actor)
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(victim.ID, 10)}}
		a.adminDeleteUser(c)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if _, err := a.db.UserByID(victim.ID); err != nil {
			t.Fatal(err)
		}
	}
}

type followupCacheHook struct {
	cache.Cache
	hook func()
}

func (c *followupCacheHook) Get(key string) ([]byte, bool) {
	if strings.HasPrefix(key, "similar:") && c.hook != nil {
		f := c.hook
		c.hook = nil
		f()
	}
	return c.Cache.Get(key)
}

func TestSimilarUsesOneGrantSnapshot(t *testing.T) {
	a, _, _ := asyncScanFixture(t)
	public, err := a.db.AddLibrary("snapshot-public", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := a.db.AddLibrary("snapshot-secret", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index := func(lib store.Library, title string) int64 {
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, title+".strm"), Status: "success", Title: title, Collection: "snapshot-series"}, 1, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	source := index(public, "snapshot-source")
	index(secret, "PRIVATE-CANDIDATE")
	viewer, err := a.db.CreateUser("snapshot-viewer", "password-1234", false)
	if err != nil {
		t.Fatal(err)
	}
	grant := func(ids ...int64) {
		if err := a.db.SetUserLibraries(viewer.ID, ids); err != nil {
			t.Fatal(err)
		}
	}
	grant(public.ID)
	a.cache = &followupCacheHook{Cache: a.cache, hook: func() { grant(public.ID, secret.ID) }}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(currentUserKey, viewer) })
	r.GET("/similar/:id", a.similar)
	request := func(wantSecret bool) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/similar/"+strconv.FormatInt(source, 10), nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE-CANDIDATE") != wantSecret {
			t.Fatalf("wantSecret=%v status=%d body=%s", wantSecret, w.Code, w.Body.String())
		}
	}
	request(false) // grant widens at cache lookup, not while filtering candidates
	request(true)  // a separately keyed broad snapshot may see the secret
	grant(public.ID)
	request(false) // narrow cached result cannot contain a broad-scope candidate
	grant(secret.ID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/similar/"+strconv.FormatInt(source, 10), nil))
	if w.Code != 404 {
		t.Fatalf("unauthorized source status=%d", w.Code)
	}
}

func TestRawDebugMasksAdminUserPasswordBodies(t *testing.T) {
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []struct{ method, path string }{{"POST", "/api/admin/users"}, {"PUT", "/api/admin/users/2"}} {
			for _, body := range []string{`{"password":"FOLLOWUP_PASSWORD","unknown":"FOLLOWUP_UNKNOWN"}`, `{"password":"FOLLOWUP_PASSWORD"`} {
				var logs bytes.Buffer
				r := gin.New()
				r.Use(requestDiagnostics(true, &logs, io.Discard)...)
				path := prefix + route.path
				r.Handle(route.method, path, func(c *gin.Context) {
					got, err := io.ReadAll(c.Request.Body)
					if err != nil || string(got) != body {
						t.Errorf("body changed: %q %v", got, err)
					}
					c.Status(400)
				})
				req := httptest.NewRequest(route.method, path, strings.NewReader(body))
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != 400 || strings.Contains(logs.String(), "FOLLOWUP_") || !strings.Contains(logs.String(), "redacted sensitive route") {
					t.Fatalf("status=%d log=%s", w.Code, logs.String())
				}
			}
		}
	}
}
