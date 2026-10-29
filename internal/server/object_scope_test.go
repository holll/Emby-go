package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// Exercise the real authentication middleware and memory cache. In particular,
// granting/revoking a library does not bump the movie content version.
func TestObjectScopeAuthorizationAndCache(t *testing.T) {
	a, ts, admin := asyncScanFixture(t)
	if err := a.db.SetSetting(settingCollectionMinMovies, "1"); err != nil {
		t.Fatal(err)
	}
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	public := libs[0]
	private, err := a.db.AddLibrary("Private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range []store.Library{public, private} {
		if err := os.WriteFile(filepath.Join(lib.Path, libraryCoverFileName), []byte("cover"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	index := func(lib store.Library, title, entity string) string {
		t.Helper()
		poster := filepath.Join(lib.Path, "poster.jpg")
		if err := os.WriteFile(poster, []byte("poster"), 0600); err != nil {
			t.Fatal(err)
		}
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, title+".strm"), SourceProtocol: "https", Status: "success", Title: title, Plot: title + " plot", Collection: entity, Genres: []string{entity}, Tags: []string{entity}, Studios: []string{entity}, PosterPath: poster, AdditionalParts: []string{filepath.Join(lib.Path, "part2.strm")}}, 1, time.Now(), []store.ActorRef{{Name: entity}})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(id, 10)
	}
	publicID := index(public, "Visible Movie", "ZVisible")
	privateID := index(private, "Private Movie", "APrivate") // sorts first globally
	create := func(name string) (string, int64) {
		t.Helper()
		if s, b := embyDo(t, ts, "POST", "/api/admin/users", admin, `{"name":"`+name+`","password":"password-1234","is_admin":false,"library_ids":[]}`); s != 201 {
			t.Fatalf("create %d %s", s, b)
		}
		s, b := embyDo(t, ts, "POST", "/Users/AuthenticateByName", "", `{"Username":"`+name+`","Pw":"password-1234"}`)
		var login struct {
			AccessToken string
			User        struct{ Id string }
		}
		if s != 200 || json.Unmarshal(b, &login) != nil {
			t.Fatalf("login %d %s", s, b)
		}
		id, err := strconv.ParseInt(login.User.Id, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return login.AccessToken, id
	}
	token, uid := create("scope-viewer")
	otherToken, _ := create("scope-other")
	userPath := "/Users/" + strconv.FormatInt(uid, 10)
	entityPaths := []string{"/Genres?Limit=1", userPath + "/Items?IncludeItemTypes=Tag&Limit=1", userPath + "/Items?IncludeItemTypes=Studio&Limit=1", userPath + "/Items?IncludeItemTypes=Person&Limit=1"}
	assertEmpty := func(path, token string) {
		t.Helper()
		s, b := embyDo(t, ts, "GET", path, token, "")
		if s != 200 || !strings.Contains(string(b), `"Items":[]`) || !strings.Contains(string(b), `"TotalRecordCount":0`) {
			t.Fatalf("empty %s %d %s", path, s, b)
		}
	}
	assertStatus := func(method, path string, want int) {
		t.Helper()
		s, b := embyDo(t, ts, method, path, token, "")
		if s != want {
			t.Fatalf("%s %s %d want %d %s", method, path, s, want, b)
		}
	}
	moviePaths := func(id string) []string {
		return []string{"/Items/" + id, "/Items/" + id + "/Images", "/Items/" + id + "/Similar", "/Videos/" + id + "/AdditionalParts"}
	}
	imagePaths := func(lib store.Library, entity string) []string {
		return []string{"/Items/" + externalLibraryID(lib.ID) + "/Images", "/Items/" + entityId("Genre", entity) + "/Images", "/Items/" + entityId("Person", entity) + "/Images", "/Items/" + boxsetID(entity) + "/Images"}
	}
	writes := func(id string) []struct{ method, path string } {
		return []struct{ method, path string }{{"POST", userPath + "/FavoriteItems/" + id}, {"DELETE", userPath + "/FavoriteItems/" + id}, {"POST", userPath + "/Items/" + id + "/Rating?Likes=true"}, {"DELETE", userPath + "/Items/" + id + "/Rating"}, {"POST", userPath + "/Items/" + id + "/HideFromResume?Hide=true"}, {"POST", userPath + "/PlayedItems/" + id}, {"DELETE", userPath + "/PlayedItems/" + id}}
	}
	assertDenied := func(id string, lib store.Library, entity string) {
		t.Helper()
		for _, p := range append(moviePaths(id), imagePaths(lib, entity)...) {
			assertStatus("GET", p, 404)
		}
		for _, w := range writes(id) {
			assertStatus(w.method, w.path, 404)
		}
	}
	for range 2 {
		assertEmpty(userPath+"/Suggestions", token)
		for _, p := range entityPaths {
			assertEmpty(p, token)
		}
		assertDenied(publicID, public, "ZVisible")
	}
	if err := a.db.SetUserLibraries(uid, []int64{public.ID}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, p := range append([]string{userPath + "/Suggestions?Limit=1"}, entityPaths...) {
			s, b := embyDo(t, ts, "GET", p, token, "")
			if s != 200 || !strings.Contains(string(b), "ZVisible") || strings.Contains(string(b), "APrivate") || strings.Contains(string(b), "Private Movie") {
				t.Fatalf("scoped %s %d %s", p, s, b)
			}
		}
		for _, p := range append(moviePaths(publicID), imagePaths(public, "ZVisible")...) {
			assertStatus("GET", p, 200)
		}
		assertDenied(privateID, private, "APrivate")
		assertEmpty("/Genres?Limit=1", otherToken)
	}
	for _, w := range writes(publicID) {
		want := 200
		if strings.Contains(w.path, "/PlayedItems/") {
			want = 204
		}
		assertStatus(w.method, w.path, want)
	}
	// Revoke after successful requests have warmed response and metadata caches.
	version := a.db.Version("g:version")
	if err := a.db.SetUserLibraries(uid, nil); err != nil {
		t.Fatal(err)
	}
	if a.db.Version("g:version") != version {
		t.Fatal("fixture must revoke without content version bump")
	}
	assertEmpty(userPath+"/Suggestions", token)
	for _, p := range entityPaths {
		assertEmpty(p, token)
	}
	assertDenied(publicID, public, "ZVisible")
	if err := a.db.SetUserLibraries(uid, []int64{private.ID}); err != nil {
		t.Fatal(err)
	}
	s, b := embyDo(t, ts, "GET", "/Genres?Limit=1", token, "")
	if s != 200 || !strings.Contains(string(b), "APrivate") || strings.Contains(string(b), "ZVisible") {
		t.Fatalf("grant swap %d %s", s, b)
	}
	assertStatus("GET", "/Items/"+privateID+"/Images", 200)
	assertStatus("GET", "/Items/"+publicID+"/Images", 404)
	// Public image bytes intentionally remain outside this authorization policy.
	s, _ = embyDo(t, ts, "GET", "/Items/"+publicID+"/Images/Primary", "", "")
	if s != 200 {
		t.Fatalf("anonymous image compatibility: %d", s)
	}
}

func TestObjectScopeHelpersFailClosed(t *testing.T) {
	a, _, _ := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	check := func() {
		t.Helper()
		if _, admin, ok := a.viewerLibraries(c); ok || admin {
			t.Fatal("missing identity must not become unrestricted")
		}
		if a.canViewLibrary(c, libs[0].ID) {
			t.Fatal("missing identity must not access library")
		}
	}
	check()
	c.Set(currentUserKey, store.User{ID: 999999, IsAdmin: true})
	check() // stale/deleted account in middleware context
	c.Set(currentUserKey, store.User{ID: 0, IsAdmin: true})
	check()
	u, err := a.db.CreateUser("scope-stale", "password-1234", false)
	if err != nil {
		t.Fatal(err)
	}
	// The middleware snapshot may predate a role change. Read current authority.
	c.Set(currentUserKey, store.User{ID: u.ID, IsAdmin: true})
	if visible, admin, ok := a.viewerLibraries(c); !ok || admin || visible == nil || len(visible) != 0 {
		t.Fatalf("stale admin context: visible=%v admin=%v ok=%v", visible, admin, ok)
	}
	if a.canViewLibrary(c, libs[0].ID) {
		t.Fatal("stale admin flag authorized library")
	}
	if err := a.db.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	check()
	c.Set(currentUserKey, store.User{ID: 1, IsAdmin: true})
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	check() // storage failures must not become unrestricted either
}
