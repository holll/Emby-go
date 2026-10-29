package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type userSettingRoute struct{ name, method, path string }

func settingRoutes(id int64) []userSettingRoute {
	return []userSettingRoute{
		{"favorite", "POST", fmt.Sprintf("/Users/1/FavoriteItems/%d", id)},
		{"rating", "POST", fmt.Sprintf("/Users/1/Items/%d/Rating?Likes=true", id)},
		{"hidden", "POST", fmt.Sprintf("/Users/1/Items/%d/HideFromResume?Hide=true", id)},
	}
}
func userSettingRequest(h http.Handler, route userSettingRoute, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(route.method, route.path, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestUserSettingsAtomicAPI(t *testing.T) {
	for op := range 3 {
		for _, stage := range []string{"global", "library", "userdata", "commit"} {
			t.Run(fmt.Sprintf("%d/%s", op, stage), func(t *testing.T) {
				f := newAdminDeleteFixture(t)
				route := settingRoutes(f.victim)[op]
				h := f.a.Handler()
				before := adminDeleteSnapshot(t, f)
				version := f.a.db.Version("g:version")
				var oldLibraryVersion int
				if err := f.db.QueryRow("SELECT value FROM kv WHERE key=?", fmt.Sprintf("lib:%d:version", f.lib)).Scan(&oldLibraryVersion); err != nil {
					t.Fatal(err)
				}
				if w := userSettingRequest(h, route, ""); w.Code != 401 {
					t.Fatalf("unauthenticated status=%d", w.Code)
				}
				if adminDeleteSnapshot(t, f) != before {
					t.Fatal("unauthenticated write")
				}
				if stage == "commit" {
					if _, err := f.db.Exec(`CREATE TABLE fixture_user_api_parent(id INTEGER PRIMARY KEY);
CREATE TABLE fixture_user_api_child(parent_id INTEGER REFERENCES fixture_user_api_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_user_api AFTER UPDATE ON userdata BEGIN INSERT INTO fixture_user_api_child VALUES(999999); END;`); err != nil {
						t.Fatal(err)
					}
				} else {
					clause := map[string]string{"global": "BEFORE UPDATE ON kv WHEN OLD.key='g:version'", "library": fmt.Sprintf("BEFORE UPDATE ON kv WHEN OLD.key='lib:%d:version'", f.lib), "userdata": "BEFORE UPDATE ON userdata"}[stage]
					if _, err := f.db.Exec("CREATE TRIGGER fail_user_api " + clause + " BEGIN SELECT RAISE(ABORT,'fixture-user-api-secret'); END"); err != nil {
						t.Fatal(err)
					}
				}
				w := userSettingRequest(h, route, "fixture-admin-delete")
				if w.Code != 500 || strings.Contains(w.Body.String(), "fixture-user-api") || w.Body.String() != "{\"code\":\"INTERNAL\",\"error\":\"user data write failed\"}" {
					t.Fatalf("write error status=%d body=%s", w.Code, w.Body.String())
				}
				if adminDeleteSnapshot(t, f) != before || f.a.db.Version("g:version") != version {
					t.Fatal("failed API write changed data or versions")
				}
				if stage == "commit" {
					var n int
					if err := f.db.QueryRow("SELECT COUNT(*) FROM fixture_user_api_child").Scan(&n); err != nil || n != 0 {
						t.Fatalf("deferred child=%d err=%v", n, err)
					}
				}
				if _, err := f.db.Exec("DROP TRIGGER fail_user_api"); err != nil {
					t.Fatal(err)
				}
				w = userSettingRequest(h, route, "fixture-admin-delete")
				var body map[string]any
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["ItemId"] != fmt.Sprint(f.victim) || body["PlayCount"] != float64(3) || body["PlaybackPositionTicks"] != float64(120) {
					t.Fatalf("retry status=%d body=%s", w.Code, w.Body.String())
				}
				data, err := f.a.db.DataUser(1, f.victim)
				if err != nil || (op == 0 && !data.IsFavorite) || (op == 1 && data.Likes != 1) || (op == 2 && !data.HideFromResume) {
					t.Fatalf("setting not saved: %+v err=%v", data, err)
				}
				var g, lib int
				if err := f.db.QueryRow("SELECT value FROM kv WHERE key='g:version'").Scan(&g); err != nil {
					t.Fatal(err)
				}
				if err := f.db.QueryRow("SELECT value FROM kv WHERE key=?", fmt.Sprintf("lib:%d:version", f.lib)).Scan(&lib); err != nil {
					t.Fatal(err)
				}
				var old int
				if _, err := fmt.Sscan(version, &old); err != nil {
					t.Fatal(err)
				}
				if g != old+1 || f.a.db.Version("g:version") != fmt.Sprint(g) || lib != oldLibraryVersion+1 {
					t.Fatalf("versions g=%d lib=%d oldg=%d", g, lib, old)
				}
			})
		}
	}
}

func TestUserSettingsAliasesAndValues(t *testing.T) {
	f := newAdminDeleteFixture(t)
	h := f.a.Handler()
	routes := []userSettingRoute{
		{"favorite", "POST", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim)},
		{"unfavorite", "DELETE", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim)},
		{"favorite", "POST", fmt.Sprintf("/emby/Users/1/FavoriteItems/%d", f.victim)},
		{"unfavorite-alias", "POST", fmt.Sprintf("/Users/1/FavoriteItems/%d/Delete", f.victim)},
		{"like", "POST", fmt.Sprintf("/Users/1/Items/%d/Rating?Likes=true", f.victim)},
		{"dislike", "POST", fmt.Sprintf("/Users/1/Items/%d/Rating?Likes=false", f.victim)},
		{"clear", "DELETE", fmt.Sprintf("/Users/1/Items/%d/Rating", f.victim)},
		{"clear-alias", "POST", fmt.Sprintf("/Users/1/Items/%d/Rating/Delete", f.victim)},
		{"hide", "POST", fmt.Sprintf("/Users/1/Items/%d/HideFromResume?Hide=true", f.victim)},
		{"show", "POST", fmt.Sprintf("/Users/1/Items/%d/HideFromResume?Hide=false", f.victim)},
	}
	for _, route := range routes {
		w := userSettingRequest(h, route, "fixture-admin-delete")
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", route.name, w.Code, w.Body.String())
		}
		data, err := f.a.db.DataUser(1, f.victim)
		if err != nil {
			t.Fatal(err)
		}
		switch route.name {
		case "favorite":
			if !data.IsFavorite {
				t.Fatal("favorite not set")
			}
		case "unfavorite", "unfavorite-alias":
			if data.IsFavorite {
				t.Fatal("favorite not cleared")
			}
		case "like":
			if data.Likes != 1 {
				t.Fatal("like")
			}
		case "dislike":
			if data.Likes != -1 {
				t.Fatal("dislike")
			}
		case "clear", "clear-alias":
			if data.Likes != 0 {
				t.Fatal("clear")
			}
		case "hide":
			if !data.HideFromResume {
				t.Fatal("hide")
			}
		case "show":
			if data.HideFromResume {
				t.Fatal("show")
			}
		}
	}
	before := adminDeleteSnapshot(t, f)
	for _, route := range []userSettingRoute{{"bad-user", "POST", fmt.Sprintf("/Users/99/FavoriteItems/%d", f.victim)}, {"bad-rating", "POST", fmt.Sprintf("/Users/1/Items/%d/Rating?Likes=unknown", f.victim)}, {"missing", "POST", "/Users/1/FavoriteItems/999999"}} {
		w := userSettingRequest(h, route, "fixture-admin-delete")
		want := 404
		if route.name == "bad-rating" {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("%s status=%d", route.name, w.Code)
		}
	}
	if adminDeleteSnapshot(t, f) != before {
		t.Fatal("rejected request wrote state")
	}
}

func TestUserSettingsConcurrentVisibilityGuard(t *testing.T) {
	f, g := newCacheReadFixture(t, nil)
	before, err := f.a.db.DataUser(1, f.victim)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/Users/:uid/FavoriteItems/:id", f.a.favorite)
	route := settingRoutes(f.victim)[0]
	w := httptest.NewRecorder()
	done := make(chan struct{})
	t.Cleanup(func() { joinCacheRead(t, g, done) })
	go func() { defer close(done); r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil)) }()
	waitCacheRead(t, g)
	if _, err := f.db.Exec("UPDATE cache_fixture_movies SET status='pending' WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.BumpVersion(f.lib); err != nil {
		t.Fatal(err)
	}
	published := f.a.db.Version("g:version")
	joinCacheRead(t, g, done)
	after, err := f.a.db.DataUser(1, f.victim)
	if w.Code != 404 || err != nil || after != before || f.a.db.Version("g:version") != published {
		t.Fatalf("hidden concurrent setting status=%d data=%+v err=%v version=%s want=%s", w.Code, after, err, f.a.db.Version("g:version"), published)
	}
}
