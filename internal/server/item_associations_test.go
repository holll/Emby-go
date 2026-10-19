package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/cache"
	"github.com/gin-gonic/gin"
)

type associationCache struct {
	cache.Cache
	responseWrites atomic.Int32
}

func (s *associationCache) Set(key string, value []byte, ttl time.Duration) {
	if strings.HasPrefix(key, "items:") || strings.HasPrefix(key, "similar:") {
		s.responseWrites.Add(1)
	}
	s.Cache.Set(key, value, ttl)
}
func associationRoutes(f adminDeleteFixture, actors bool) []string {
	routes := []string{"/Items", "/Users/1/Items/Resume", "/Users/1/Items/Latest", "/Users/1/Suggestions", fmt.Sprintf("/Items/%d/Similar", f.victim)}
	if !actors {
		routes = append(routes, "/api/admin/items", fmt.Sprintf("/api/admin/items/%d/detail", f.victim))
	}
	return routes
}
func associationGET(h http.Handler, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestItemAssociationErrorsDoNotReturnOrCacheDefaults(t *testing.T) {
	for _, fault := range []string{"userdata_scan", "actors_scan", "actors_query"} {
		t.Run(fault, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			// Ensure a real nonempty similar result independently of feature weights.
			if _, err := f.db.Exec("UPDATE movie_features SET weight=20"); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{f.victim, f.survivor} {
				if _, err := f.a.db.SetIndexedFavoriteDataForUser(1, id, true); err != nil {
					t.Fatal(err)
				}
			}
			recorder := &associationCache{Cache: cache.NewMemory(64)}
			f.a.cache = recorder
			h := f.a.Handler()
			routes := associationRoutes(f, fault != "userdata_scan")
			good := make(map[string]string)
			for _, path := range routes {
				recorder.Clear()
				w := associationGET(h, path, "fixture-admin-delete")
				if w.Code != 200 {
					t.Fatalf("baseline %s: %d %s", path, w.Code, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), "true") {
					t.Fatalf("baseline must include genuine user/actor data: %s", w.Body.String())
				}
				good[path] = w.Body.String()
			}
			var breakSQL, repairSQL string
			switch fault {
			case "userdata_scan":
				breakSQL = "UPDATE userdata SET position_ticks='private-association-canary'"
				repairSQL = "UPDATE userdata SET position_ticks=120"
			case "actors_scan":
				breakSQL = "ALTER TABLE movie_actors RENAME TO fixture_association_actors; CREATE VIEW movie_actors AS SELECT movie_id,NULL AS actor_name FROM fixture_association_actors"
				repairSQL = "DROP VIEW movie_actors; ALTER TABLE fixture_association_actors RENAME TO movie_actors"
			case "actors_query":
				breakSQL = "ALTER TABLE movie_actors RENAME COLUMN actor_name TO private_association_column"
				repairSQL = "ALTER TABLE movie_actors RENAME COLUMN private_association_column TO actor_name"
			}
			if _, err := f.db.Exec(breakSQL); err != nil {
				t.Fatal(err)
			}
			before := f.a.db.Version("g:version")
			for _, path := range routes {
				recorder.Clear()
				recorder.responseWrites.Store(0)
				if w := associationGET(h, path, ""); w.Code != 401 {
					t.Fatalf("unauthenticated %s: %d", path, w.Code)
				}
				w := associationGET(h, path, "fixture-admin-delete")
				if w.Code != 500 || w.Body.String() != `{"code":"ITEM_DATA_UNAVAILABLE","error":"item data unavailable"}` {
					t.Fatalf("%s %s: %d %s", fault, path, w.Code, w.Body.String())
				}
				if recorder.responseWrites.Load() != 0 {
					t.Fatalf("%s cached incomplete response", path)
				}
				if f.a.db.Version("g:version") != before {
					t.Fatal("read changed version")
				}
			}
			if _, err := f.db.Exec(repairSQL); err != nil {
				t.Fatal(err)
			}
			// Do not clear between the failed request and retry: no poisoned response
			// may prevent recovery in the same cache namespace.
			for _, path := range routes {
				w := associationGET(h, path, "fixture-admin-delete")
				if w.Code != 200 || w.Body.String() != good[path] {
					t.Fatalf("recovery %s: %d %s; want %s", path, w.Code, w.Body.String(), good[path])
				}
			}
			if recorder.responseWrites.Load() == 0 {
				t.Fatal("successful list/similar response did not cache")
			}
		})
	}
}

func TestItemAssociationMissingRowsRemainSuccessful(t *testing.T) {
	f := newAdminDeleteFixture(t)
	if _, err := f.db.Exec("DELETE FROM movie_actors; DELETE FROM userdata"); err != nil {
		t.Fatal(err)
	}
	h := f.a.Handler()
	for _, path := range associationRoutes(f, false) {
		w := associationGET(h, path, "fixture-admin-delete")
		if w.Code != 200 || strings.Contains(w.Body.String(), "ITEM_DATA_UNAVAILABLE") {
			t.Fatalf("missing association %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestItemAssociationEmptyResponseDoesNotReadClosedDatabase(t *testing.T) {
	a, _, _ := authorityApp(t)
	a.Close()
	r := gin.New()
	r.GET("/empty", func(c *gin.Context) { a.respondItems(c, nil, 0, 0, "fixture-empty") })
	w := associationGET(r, "/empty", "")
	if w.Code != 200 || w.Body.String() != `{"Items":[],"StartIndex":0,"TotalRecordCount":0}` {
		t.Fatalf("empty: %d %s", w.Code, w.Body.String())
	}
	if _, ok := a.cache.Get("items:fixture-empty"); !ok {
		t.Fatal("empty success should retain ordinary caching")
	}
}
