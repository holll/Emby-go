package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/store"
)

func TestMovieParentRoundTrip(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	first := libs[0]
	second, err := a.db.AddLibrary("Other parent", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, lib := range []store.Library{first, second} {
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, "parent.strm"), Title: lib.Name, Status: "success", Collection: "Parent collection"}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	type dto struct {
		ID          string   `json:"Id"`
		Parent      string   `json:"ParentId"`
		Type        string   `json:"Type"`
		IsFolder    bool     `json:"IsFolder"`
		Collections []string `json:"CollectionIds"`
	}
	get := func(path, credential string) dto {
		t.Helper()
		status, body := embyDo(t, ts, http.MethodGet, path, credential, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, status, body)
		}
		var d dto
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	list := func(path, credential string) []dto {
		t.Helper()
		status, body := embyDo(t, ts, http.MethodGet, path, credential, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, status, body)
		}
		var r struct{ Items []dto }
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
		return r.Items
	}
	for i, lib := range []store.Library{first, second} {
		want := externalLibraryID(lib.ID)
		for _, prefix := range []string{"", "/emby"} {
			for _, route := range []string{"/Items/", "/Users/1/Items/"} {
				path := prefix + route + strconv.FormatInt(ids[i], 10)
				// Repeat to cover both generation and a warm detail response cache.
				for attempt := 0; attempt < 2; attempt++ {
					d := get(path, token)
					if d.ID != strconv.FormatInt(ids[i], 10) || d.Type != "Movie" || d.Parent != want {
						t.Fatalf("%s: unexpected movie DTO %+v; want parent=%s", path, d, want)
					}
					if len(d.Collections) != 1 || d.Collections[0] != boxsetID("Parent collection") {
						t.Fatalf("collection IDs changed: %+v", d)
					}
					parent := get(prefix+route+d.Parent, token)
					if parent.ID != want || parent.Type != "CollectionFolder" || !parent.IsFolder {
						t.Fatalf("parent not resolvable as library: %+v", parent)
					}
				}
			}
			views := list(prefix+"/Users/1/Views", token)
			found := false
			for _, v := range views {
				if v.ID == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("parent %s absent from views %+v", want, views)
			}
			for _, parent := range []string{want, strconv.FormatInt(lib.ID, 10)} {
				movies := list(prefix+"/Users/1/Items?IncludeItemTypes=Movie&ParentId="+parent, token)
				if len(movies) != 1 || movies[0].ID != strconv.FormatInt(ids[i], 10) || movies[0].Parent != want {
					t.Fatalf("parent=%s crossed library or changed legacy query: %+v", parent, movies)
				}
			}
		}
	}
	// User ACL applies equally to the movie, its returned parent, and membership.
	create := fmt.Sprintf(`{"name":"parent-viewer","password":"visitor-pass-1","is_admin":false,"library_ids":[%d]}`, first.ID)
	status, body := embyDo(t, ts, http.MethodPost, "/api/admin/users", token, create)
	if status != http.StatusCreated {
		t.Fatalf("create user: %d %s", status, body)
	}
	status, body = embyDo(t, ts, http.MethodPost, "/emby/Users/AuthenticateByName", "", `{"Username":"parent-viewer","Pw":"visitor-pass-1"}`)
	var auth struct {
		AccessToken string
		User        struct {
			ID string `json:"Id"`
		}
	}
	if status != http.StatusOK || json.Unmarshal(body, &auth) != nil || auth.AccessToken == "" || auth.User.ID == "" {
		t.Fatalf("login: %d %s", status, body)
	}
	uid := auth.User.ID
	d := get("/emby/Users/"+uid+"/Items/"+strconv.FormatInt(ids[0], 10), auth.AccessToken)
	if d.Parent != externalLibraryID(first.ID) {
		t.Fatalf("viewer parent=%s", d.Parent)
	}
	get("/emby/Users/"+uid+"/Items/"+d.Parent, auth.AccessToken)
	for _, path := range []string{
		"/emby/Items/" + strconv.FormatInt(ids[1], 10),
		"/emby/Users/" + uid + "/Items/" + externalLibraryID(second.ID),
		"/emby/Items/" + externalLibraryID(second.ID),
		"/emby/Users/1/Items/" + d.Parent,
	} {
		status, _ := embyDo(t, ts, http.MethodGet, path, auth.AccessToken, "")
		if status != http.StatusNotFound && status != http.StatusForbidden {
			t.Fatalf("unauthorized parent/item path=%s status=%d", path, status)
		}
	}
	hidden := list("/emby/Users/"+uid+"/Items?IncludeItemTypes=Movie&ParentId="+externalLibraryID(second.ID), auth.AccessToken)
	if len(hidden) != 0 {
		t.Fatalf("private library query leaked: %+v", hidden)
	}
	visible := list("/emby/Users/"+uid+"/Items?IncludeItemTypes=Movie", auth.AccessToken)
	if len(visible) != 1 || visible[0].ID != strconv.FormatInt(ids[0], 10) || visible[0].Parent != externalLibraryID(first.ID) {
		t.Fatalf("viewer list: %+v", visible)
	}
	status, _ = embyDo(t, ts, http.MethodGet, "/emby/Items/"+d.Parent, "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous parent status=%d", status)
	}
}

// Model a Redis instance that survives a binary upgrade and still holds old DTOs.
// Any access to a pre-revision response key returns a valid, deliberately stale DTO.
type parentUpgradeCache struct {
	cache.Cache
	legacy         []byte
	legacyReads    atomic.Int32
	warmReads      atomic.Int32
	responseWrites atomic.Int32
}

func (s *parentUpgradeCache) Get(key string) ([]byte, bool) {
	for _, prefix := range []string{"item:", "items:", "similar:"} {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !strings.HasPrefix(key, prefix+movieDTOCacheRevision) {
			s.legacyReads.Add(1)
			return s.legacy, true
		}
		b, ok := s.Cache.Get(key)
		if ok {
			s.warmReads.Add(1)
		}
		return b, ok
	}
	return s.Cache.Get(key)
}
func (s *parentUpgradeCache) Set(key string, b []byte, ttl time.Duration) {
	if strings.HasPrefix(key, "item:") || strings.HasPrefix(key, "items:") || strings.HasPrefix(key, "similar:") {
		s.responseWrites.Add(1)
	}
	s.Cache.Set(key, b, ttl)
}

func TestMovieParentCacheUpgrade(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	a.startupWG.Wait()
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	lib := libs[0]
	var victim int64
	for i := 0; i < 2; i++ {
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("cache-%d.strm", i)), Title: fmt.Sprintf("Parent cache %d", i), Status: "success", Year: 2020, Genres: []string{"Drama"}}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			victim = id
		}
	}
	// Item and list shapes deliberately coexist in the poison body; either old
	// route would expose a raw parent while still returning HTTP 200 / valid JSON.
	old := fmt.Sprintf(`{"Id":"%d","Type":"Movie","ParentId":"%d","Items":[{"Id":"%d","Type":"Movie","ParentId":"%d"}],"TotalRecordCount":1}`, victim, lib.ID, victim, lib.ID)
	recorder := &parentUpgradeCache{Cache: cache.NewMemory(64), legacy: []byte(old)}
	a.cache = recorder
	version := a.db.Version("g:version")
	for _, path := range []string{
		fmt.Sprintf("/emby/Users/1/Items/%d", victim),
		"/emby/Users/1/Items?IncludeItemTypes=Movie",
		fmt.Sprintf("/emby/Items/%d/Similar?Limit=1", victim),
	} {
		writes, warm := recorder.responseWrites.Load(), recorder.warmReads.Load()
		for n := 0; n < 2; n++ {
			status, body := embyDo(t, ts, http.MethodGet, path, token, "")
			if status != 200 {
				t.Fatalf("%s: %d %s", path, status, body)
			}
			var d struct {
				Type   string
				Parent string `json:"ParentId"`
				Items  []struct {
					Type   string
					Parent string `json:"ParentId"`
				}
			}
			if err := json.Unmarshal(body, &d); err != nil {
				t.Fatal(err)
			}
			if d.Type == "Movie" {
				if d.Parent != externalLibraryID(lib.ID) {
					t.Fatalf("stale parent: %s", body)
				}
			} else {
				if len(d.Items) == 0 {
					t.Fatalf("empty cache fixture response: %s", body)
				}
				for _, item := range d.Items {
					if item.Type != "Movie" || item.Parent != externalLibraryID(lib.ID) {
						t.Fatalf("stale list parent: %s", body)
					}
				}
			}
		}
		if recorder.responseWrites.Load() != writes+1 || recorder.warmReads.Load() != warm+1 {
			t.Fatalf("%s: response did not write once and hit once: writes=%d warm=%d", path, recorder.responseWrites.Load()-writes, recorder.warmReads.Load()-warm)
		}
	}
	if recorder.legacyReads.Load() != 0 {
		t.Fatalf("read %d pre-upgrade response keys", recorder.legacyReads.Load())
	}
	if a.db.Version("g:version") != version {
		t.Fatal("DTO upgrade changed media content version")
	}
}

func TestMoviePartParentAndProgressCache(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	lib := libs[0]
	id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, "main.strm"), SourceProtocol: "https", Title: "Multipart parent", Status: "success", RuntimeSeconds: 120, AdditionalParts: []string{filepath.Join(lib.Path, "part2.strm")}}, 1, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(rawID string) map[string]any {
		t.Helper()
		status, body := embyDo(t, ts, http.MethodGet, "/emby/Users/1/Items/"+rawID, token, "")
		if status != 200 {
			t.Fatalf("item %s: %d %s", rawID, status, body)
		}
		var d map[string]any
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	rawID := strconv.FormatInt(id, 10)
	partID := virtualPartID(id, 2)
	part := get(partID)
	if part["ParentId"] != rawID {
		t.Fatalf("part must retain main movie parent: %+v", part)
	}
	main := get(rawID)
	get(rawID) // Build and hit the actual new detail cache key.
	if main["ParentId"] != externalLibraryID(lib.ID) {
		t.Fatalf("main parent: %+v", main)
	}
	parent := get(externalLibraryID(lib.ID))
	if parent["Type"] != "CollectionFolder" {
		t.Fatalf("parent chain did not reach library: %+v", parent)
	}
	before := a.db.Version("g:version")
	status, body := embyDo(t, ts, http.MethodPost, "/emby/Sessions/Playing/Progress", token, fmt.Sprintf(`{"ItemId":%q,"PositionTicks":123000000,"PlaySessionId":"parent-cache-progress"}`, partID))
	if status != 204 {
		t.Fatalf("progress: %d %s", status, body)
	}
	if a.db.Version("g:version") != before {
		t.Fatal("progress changed global media version")
	}
	updated := get(rawID)
	data, ok := updated["UserData"].(map[string]any)
	if !ok || data["PlaybackPositionTicks"] != float64(123000000) || data["PlayCount"] != float64(0) {
		t.Fatalf("progress failed to invalidate canonical detail cache: %+v", updated)
	}
	if updated["ParentId"] != externalLibraryID(lib.ID) || get(partID)["ParentId"] != rawID {
		t.Fatal("progress changed parent semantics")
	}
}
