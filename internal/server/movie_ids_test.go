package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestMovieIdsAndExcludeItemTypesCompatibility(t *testing.T) {
	a, ts, adminToken := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	first := libs[0]
	second, err := a.db.AddLibrary("Private IDs library", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	viewer := seedFixtureUser(t, a, "ids-viewer", "password-1234", false)
	if err := updateFixtureUser(t, a, viewer.ID, store.AdminUserUpdate{LibraryIDs: fixtureValue([]int64{first.ID})}); err != nil {
		t.Fatal(err)
	}
	const viewerToken = "movie-ids-viewer"
	if err := saveFixtureSession(t, a, viewerToken, viewer.ID, a.sessionExpiry(time.Now())); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64)
	for i, tc := range []struct {
		name string
		lib  store.Library
		coll string
	}{
		{"Alpha", first, "Pair"},
		{"Beta", first, "Pair"},
		{"Solo", first, "Solo"},
		{"Private", second, "Pair"},
		{"Uncollected", first, ""},
	} {
		id, err := a.db.IndexMovie(store.Movie{
			LibraryID: tc.lib.ID, SourcePath: filepath.Join(tc.lib.Path, fmt.Sprintf("movie-ids-%d.strm", i)),
			Title: tc.name, SortName: tc.name, Collection: tc.coll, Status: "success",
		}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids[tc.name] = id
	}
	get := func(path, token string) (int, []string, int) {
		t.Helper()
		status, raw := embyDo(t, ts, http.MethodGet, path, token, "")
		var r struct {
			Items []struct {
				Name string
			}
			Total int `json:"TotalRecordCount"`
		}
		if status != http.StatusOK || json.Unmarshal(raw, &r) != nil || r.Items == nil {
			t.Fatalf("%s => %d %s", path, status, raw)
		}
		names := make([]string, 0, len(r.Items))
		for _, item := range r.Items {
			names = append(names, item.Name)
		}
		return status, names, r.Total
	}
	movieID := func(name string) string { return strconv.FormatInt(ids[name], 10) }
	tests := []struct {
		name      string
		path      string
		token     string
		wantNames []string
		total     int
	}{
		{"movie id", "/Items?IncludeItemTypes=Movie&Ids=" + movieID("Alpha"), viewerToken, []string{"Alpha"}, 1},
		{"movie ids union repeated and comma", "/emby/Users/2/Items?IncludeItemTypes=Video&Ids=" + movieID("Alpha") + "&Ids=" + url.QueryEscape(" "+movieID("Beta")+",invalid,999999"), viewerToken, []string{"Alpha", "Beta"}, 2},
		{"no grant movie id", "/Items?IncludeItemTypes=Movie&Ids=" + movieID("Private"), viewerToken, []string{}, 0},
		{"admin movie id", "/Items?IncludeItemTypes=Movie&Ids=" + movieID("Private"), adminToken, []string{"Private"}, 1},
		{"only invalid ids", "/Items?IncludeItemTypes=Movie&Ids=invalid,-1,0,999999", viewerToken, []string{}, 0},
		{"explicit empty ids", "/Items?IncludeItemTypes=Movie&Ids=", viewerToken, []string{}, 0},
		{"empty token comma only", "/Items?IncludeItemTypes=Movie&Ids=%20,%20", viewerToken, []string{}, 0},
		{"parent intersects ids", "/Items?IncludeItemTypes=Movie&ParentId=" + url.QueryEscape(boxsetID("Solo")) + "&Ids=" + movieID("Alpha"), viewerToken, []string{}, 0},
		{"ids before paging", "/Items?IncludeItemTypes=Movie&Ids=" + movieID("Beta") + "&StartIndex=1&Limit=1", viewerToken, []string{}, 1},
		{"exclude movie", "/Items?IncludeItemTypes=Movie&ExcludeItemTypes=Movie", viewerToken, []string{}, 0},
		{"exclude video", "/Items?IncludeItemTypes=Video&ExcludeItemTypes=Video", viewerToken, []string{}, 0},
		{"exclude movie hides video alias", "/Items?IncludeItemTypes=Video&ExcludeItemTypes=Movie", viewerToken, []string{}, 0},
		{"exclude unsupported does not hide movie", "/Items?IncludeItemTypes=Movie&ExcludeItemTypes=Series", viewerToken, []string{"Alpha", "Beta", "Solo", "Uncollected"}, 4},
		{"folder exclusion does not hide movie", "/Items?IncludeItemTypes=Movie&ExcludeItemTypes=Folder,CollectionFolder,BoxSet", viewerToken, []string{"Alpha", "Beta", "Solo", "Uncollected"}, 4},
		{"ids excludes boxset", "/Items?IncludeItemTypes=BoxSet&Ids=" + movieID("Alpha"), viewerToken, []string{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, names, total := get(tc.path, tc.token)
			if !reflect.DeepEqual(names, tc.wantNames) || total != tc.total {
				t.Fatalf("got=%v total=%d want=%v total=%d", names, total, tc.wantNames, tc.total)
			}
		})
	}
	status, raw := embyDo(t, ts, http.MethodGet, "/Users/1/Items", viewerToken, "")
	if status != http.StatusNotFound {
		t.Fatalf("cross user => %d %s", status, raw)
	}
	if err := updateFixtureUser(t, a, viewer.ID, store.AdminUserUpdate{LibraryIDs: fixtureValue([]int64{})}); err != nil {
		t.Fatal(err)
	}
	_, names, total := get("/Items?IncludeItemTypes=Movie", viewerToken)
	if !reflect.DeepEqual(names, []string{}) || total != 0 {
		t.Fatalf("no grants => %v total=%d", names, total)
	}
}
