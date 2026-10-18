package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/cache"
)

// Unlike associationCache, this records every Set, including entity/art caches.
type listFailureCache struct {
	cache.Cache
	writes atomic.Int32
}

func (s *listFailureCache) Set(key string, value []byte, ttl time.Duration) {
	s.writes.Add(1)
	s.Cache.Set(key, value, ttl)
}

func TestListPrimaryReadFailuresArePrivateAndRecoverable(t *testing.T) {
	for _, fault := range []string{"movies_query", "movies_scan", "similar_candidates_query", "similar_candidates_scan", "libraries_query"} {
		t.Run(fault, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			if _, err := f.db.Exec(`UPDATE movies SET collection='Fixture collection'; UPDATE movie_features SET weight=20`); err != nil {
				t.Fatal(err)
			}
			recorder := &listFailureCache{Cache: cache.NewMemory(64)}
			f.a.cache = recorder
			h := f.a.Handler()
			similar := fmt.Sprintf("/Items/%d/Similar", f.victim)
			routes := []string{"/Items", "/Items/Counts", "/Users/1/Items/Resume", "/Users/1/Items/Latest", "/Users/1/Suggestions", similar}
			if fault == "movies_query" {
				routes = append(routes, "/Items?ParentId=boxsets&IncludeItemTypes=BoxSet", "/Items?ParentId="+url.QueryEscape(boxsetID("Fixture collection")), "/Genres", "/Items?IncludeItemTypes=Tag", "/Items?IncludeItemTypes=Studio", "/Items?IncludeItemTypes=Person")
			}
			if strings.HasPrefix(fault, "similar_") {
				routes = []string{similar}
			}
			if fault == "libraries_query" {
				routes = []string{"/Users/1/Views"}
			}
			good := map[string]string{}
			for _, path := range routes {
				recorder.Clear()
				w := associationGET(h, path, "fixture-admin-delete")
				if w.Code != http.StatusOK {
					t.Fatalf("baseline %s: %d %s", path, w.Code, w.Body.String())
				}
				good[path] = w.Body.String()
				if path == similar && !strings.Contains(w.Body.String(), `"Id":"`+fmt.Sprint(f.survivor)+`"`) {
					t.Fatalf("similar must include actual candidate: %s", w.Body.String())
				}
			}
			var breakSQL, repairSQL string
			switch fault {
			case "libraries_query":
				breakSQL = "ALTER TABLE libraries RENAME TO fixture_private_query_libraries"
				repairSQL = "ALTER TABLE fixture_private_query_libraries RENAME TO libraries"
			case "movies_query":
				breakSQL = "ALTER TABLE movies RENAME TO fixture_private_query_movies"
				repairSQL = "ALTER TABLE fixture_private_query_movies RENAME TO movies"
			case "movies_scan":
				breakSQL = "UPDATE movies SET year='private-movie-year-canary'"
				repairSQL = "UPDATE movies SET year=0"
			case "similar_candidates_query":
				breakSQL = "ALTER TABLE movie_features RENAME TO fixture_private_query_features"
				repairSQL = "ALTER TABLE fixture_private_query_features RENAME TO movie_features"
			case "similar_candidates_scan":
				breakSQL = fmt.Sprintf("UPDATE movies SET year='private-candidate-year-canary' WHERE id=%d", f.survivor)
				repairSQL = fmt.Sprintf("UPDATE movies SET year=0 WHERE id=%d", f.survivor)
			}
			if _, err := f.db.Exec(breakSQL); err != nil {
				t.Fatal(err)
			}
			before := f.a.db.Version("g:version")
			for _, path := range routes {
				recorder.Clear()
				recorder.writes.Store(0)
				if w := associationGET(h, path, ""); w.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated %s=%d", path, w.Code)
				}
				w := associationGET(h, path, "fixture-admin-delete")
				if w.Code != 500 || w.Body.String() != `{"code":"ITEM_DATA_UNAVAILABLE","error":"item data unavailable"}` {
					t.Fatalf("%s %s: %d %s", fault, path, w.Code, w.Body.String())
				}
				if recorder.writes.Load() != 0 || f.a.db.Version("g:version") != before {
					t.Fatalf("failed read cached a response or changed version: %s", path)
				}
			}
			if _, err := f.db.Exec(repairSQL); err != nil {
				t.Fatal(err)
			}
			for _, path := range routes {
				w := associationGET(h, path, "fixture-admin-delete")
				if w.Code != http.StatusOK || w.Body.String() != good[path] {
					t.Fatalf("recovery %s: %d %s want %s", path, w.Code, w.Body.String(), good[path])
				}
			}
		})
	}
}

func TestSimilarMissingHiddenAndInvalidRemainNotFound(t *testing.T) {
	f := newAdminDeleteFixture(t)
	if _, err := f.db.Exec("UPDATE movies SET status='pending' WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	h := f.a.Handler()
	for _, id := range []string{"invalid", "999999", fmt.Sprint(f.victim)} {
		w := associationGET(h, "/Items/"+id+"/Similar", "fixture-admin-delete")
		if w.Code != http.StatusNotFound || w.Body.String() != `{"code":"NOT_FOUND","error":"not found"}` {
			t.Fatalf("id=%s: %d %s", id, w.Code, w.Body.String())
		}
	}
}
