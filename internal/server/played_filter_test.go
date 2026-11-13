package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestEmbyIsPlayedFilter(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil || len(libs) != 1 {
		t.Fatalf("libraries: %v %v", libs, err)
	}
	second, err := a.db.AddLibrary("Second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := seedFixtureUser(t, a, "other-player", "other-password", true)
	const otherToken = "played-filter-other-session"
	if err := saveFixtureSession(t, a, otherToken, other.ID, a.sessionExpiry(time.Now())); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, name := range []string{"A played", "B unplayed", "C absent", "D other", "E second"} {
		lib := libs[0]
		if name == "E second" {
			lib = second
		}
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, name+".strm"), Title: name, Status: "success", Collection: "Shared"}, 0, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	for _, name := range []string{"A played", "E second"} {
		if _, err := a.db.SetIndexedPlayedDataForUser(1, ids[name], true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.SetIndexedPlayedDataForUser(1, ids["B unplayed"], false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.SetIndexedPlayedDataForUser(other.ID, ids["D other"], true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.SetIndexedFavoriteDataForUser(1, ids["A played"], true); err != nil {
		t.Fatal(err)
	}
	// Simulate persisted results from before IsPlayed had SQL semantics. Both
	// unrestricted and played queries must use the new list-only cache keys.
	oldKeys := make(map[string]string)
	for _, filter := range []string{"", "IsPlayed"} {
		key := "items:" + strings.Join([]string{movieDTOCacheRevision, a.db.Version("g:version"), "1", "all", "0", "", "", "", "", "", "", filter, "", "", "", "false", "SortName", "Ascending", "", "0", "100"}, "|")
		oldKeys[filter] = key
		a.cache.Set(key, []byte(`{"Items":[{"Name":"obsolete cached result"}],"TotalRecordCount":1}`), time.Minute)
	}
	for _, tc := range []struct {
		name, path, token string
		want              []string
		total             int
	}{
		{name: "without filter unchanged", path: "/emby/Users/1/Items", want: []string{"A played", "B unplayed", "C absent", "D other", "E second"}, total: 5},
		{name: "root played", path: "/emby/Users/1/Items?Filters=IsPlayed", want: []string{"A played", "E second"}, total: 2},
		{name: "case insensitive", path: "/emby/Users/1/Items?Filters=iSpLaYeD", want: []string{"A played", "E second"}, total: 2},
		{name: "page after SQL filter", path: "/emby/Users/1/Items?Filters=IsPlayed&Limit=1&StartIndex=1", want: []string{"E second"}, total: 2},
		{name: "library", path: "/emby/Users/1/Items?Filters=IsPlayed&ParentId=" + externalLibraryID(libs[0].ID), want: []string{"A played"}, total: 1},
		{name: "collection", path: "/emby/Users/1/Items?Filters=IsPlayed&ParentId=" + url.QueryEscape(boxsetID("Shared")), want: []string{"A played", "E second"}, total: 2},
		{name: "collection view recursive", path: "/emby/Users/1/Items?Filters=IsPlayed&ParentId=boxsets&Recursive=true", want: []string{"Shared"}, total: 1},
		{name: "favorite combination", path: "/emby/Users/1/Items?Filters=IsPlayed,IsFavorite", want: []string{"A played"}, total: 1},
		{name: "contradictory played", path: "/emby/Users/1/Items?Filters=IsPlayed,IsUnplayed"},
		{name: "unplayed unchanged", path: "/emby/Users/1/Items?Filters=IsUnplayed", want: []string{"B unplayed", "C absent", "D other"}, total: 3},
		{name: "other user isolated", path: fmt.Sprintf("/emby/Users/%d/Items?Filters=IsPlayed", other.ID), token: otherToken, want: []string{"D other"}, total: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credential := tc.token
			if credential == "" {
				credential = token
			}
			query, err := url.Parse(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			params := query.Query()
			params.Set("SortBy", "SortName")
			params.Set("SortOrder", "Ascending")
			params.Set("IncludeItemTypes", "Movie")
			query.RawQuery = params.Encode()
			status, raw := embyDo(t, ts, http.MethodGet, query.String(), credential, "")
			if status != http.StatusOK {
				t.Fatalf("status=%d body=%s", status, raw)
			}
			var page struct {
				Items []struct {
					Name string
				}
				TotalRecordCount int
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range page.Items {
				got = append(got, item.Name)
			}
			if !slices.Equal(got, tc.want) || page.TotalRecordCount != tc.total {
				t.Fatalf("items=%v total=%d, want %v total=%d", got, page.TotalRecordCount, tc.want, tc.total)
			}
		})
	}
	for filter, suffix := range map[string]string{"": "false", "IsPlayed": "true"} {
		if _, ok := a.cachedBody(oldKeys[filter] + "|" + suffix); !ok {
			t.Errorf("missing isolated %s cache entry", suffix)
		}
	}
}
