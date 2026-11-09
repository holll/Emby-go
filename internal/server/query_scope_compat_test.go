package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestQueryUserAndParentScopeCompatibility(t *testing.T) {
	a, ts, adminToken := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	first := libs[0]
	private, err := a.db.AddLibrary("Private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"Alpha", "Beta", "Solo", "Private", "Uncollected"} {
		lib, collection := first, "Pair"
		if i == 2 {
			collection = "Solo"
		}
		if i == 3 {
			lib = private
		}
		if i == 4 {
			collection = ""
		}
		if _, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("scope-%d.strm", i)), Title: name, Collection: collection, Genres: []string{name}, Tags: []string{name}, Status: "success"}, 1, time.Unix(1, 0), nil); err != nil {
			t.Fatal(err)
		}
	}
	viewer := seedFixtureUser(t, a, "scoped-viewer", "password-1234", false)
	if err := a.db.SetUserLibraries(viewer.ID, []int64{first.ID}); err != nil {
		t.Fatal(err)
	}
	const token = "query-scope-viewer"
	if err := a.db.SaveAccessTokenForUser(token, viewer.ID, a.sessionExpiry(time.Now())); err != nil {
		t.Fatal(err)
	}
	uid := strconv.FormatInt(viewer.ID, 10)
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items?IncludeItemTypes=Movie", "/Genres?Limit=100", "/Shows/NextUp?Limit=100", "/Items/Counts?"} {
			for _, tc := range []struct {
				user   string
				status int
			}{{uid, 200}, {"1", 404}, {"999999", 404}, {"invalid", 404}} {
				t.Run(prefix+route+"/UserId="+tc.user, func(t *testing.T) {
					status, raw := embyDo(t, ts, http.MethodGet, prefix+route+"&UserId="+tc.user, token, "")
					if status != tc.status {
						t.Fatalf("status=%d want=%d body=%s", status, tc.status, raw)
					}
					if status == 200 {
						var r struct{ Items []struct{ Name string } }
						if err := json.Unmarshal(raw, &r); err != nil {
							t.Fatal(err)
						}
						for _, item := range r.Items {
							if item.Name == "Private" {
								t.Fatalf("private library leaked: %s", raw)
							}
						}
						if strings.HasSuffix(route, "Counts?") {
							var counts map[string]int
							if err := json.Unmarshal(raw, &counts); err != nil {
								t.Fatal(err)
							}
							if counts["MovieCount"] != 4 || counts["BoxSetCount"] != 1 || counts["ItemCount"] != counts["MovieCount"]+counts["BoxSetCount"] {
								t.Fatalf("counts=%v", counts)
							}
						}
					}
				})
			}
		}
		for _, parent := range []string{"unknown", "-1", strconv.FormatInt(libraryIDBase, 10), "boxset:"} {
			t.Run(prefix+"/Genres/"+parent, func(t *testing.T) {
				status, raw := embyDo(t, ts, http.MethodGet, prefix+"/Genres?ParentId="+url.QueryEscape(parent), token, "")
				var r struct {
					Items []any
					Total int `json:"TotalRecordCount"`
				}
				if status != 200 || json.Unmarshal(raw, &r) != nil || r.Items == nil || len(r.Items) != 0 || r.Total != 0 {
					t.Fatalf("unknown parent must not broaden: %d %s", status, raw)
				}
			})
		}
		for _, credential := range []string{adminToken, token} {
			for _, tc := range []struct {
				parent string
				want   []string
			}{
				{"unknown", []string{}}, {"-1", []string{}}, {"boxset:", []string{}},
				{boxsetID("missing"), []string{}}, {boxsetID("Pair"), []string{"Private", "Beta", "Alpha"}},
				{"boxsets", []string{"Private", "Solo", "Beta", "Alpha"}},
				{externalLibraryID(private.ID), []string{"Private"}},
			} {
				want := tc.want
				if credential == token {
					if tc.parent == boxsetID("Pair") {
						want = []string{"Beta", "Alpha"}
					}
					if tc.parent == "boxsets" {
						want = []string{"Solo", "Beta", "Alpha"}
					}
					if tc.parent == externalLibraryID(private.ID) {
						want = []string{}
					}
				}
				t.Run(prefix+"/Latest/"+tc.parent+"/"+credential, func(t *testing.T) {
					path := prefix + "/Users/"
					if credential == adminToken {
						path += "1"
					} else {
						path += uid
					}
					path += "/Items/Latest?ParentId=" + url.QueryEscape(tc.parent)
					status, raw := embyDo(t, ts, http.MethodGet, path, credential, "")
					var items []struct{ Name string }
					if status != 200 || json.Unmarshal(raw, &items) != nil || items == nil {
						t.Fatalf("Latest must remain an array: %d %s", status, raw)
					}
					names := make([]string, 0, len(items))
					for _, item := range items {
						names = append(names, item.Name)
					}
					if !reflect.DeepEqual(names, want) {
						t.Fatalf("got=%v want=%v raw=%s", names, want, raw)
					}
				})
			}
		}
	}
}

func TestEntitySearchTermCompatibility(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"Alpha", "Beta", "Alpine"} {
		if _, err := a.db.IndexMovie(store.Movie{LibraryID: libs[0].ID, SourcePath: filepath.Join(libs[0].Path, fmt.Sprintf("entity-search-%d.strm", i)), Title: name, Genres: []string{name}, Tags: []string{name}, Studios: []string{name}, Status: "success"}, 1, time.Unix(1, 0), []store.ActorRef{{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Genres?Limit=1", "/Items?IncludeItemTypes=Genre&Limit=1", "/Users/1/Items?IncludeItemTypes=Tag&Limit=1", "/Items?IncludeItemTypes=Studio&Limit=1", "/Items?IncludeItemTypes=Person&Limit=1"} {
			for _, tc := range []struct {
				query string
				total int
				name  string
			}{
				{"", 3, "Alpha"}, {"&SearchTerm=not-present", 0, ""}, {"&SearchTerm=ALP", 2, "Alpha"}, {"&SearchTerm=ALP&StartIndex=1", 2, "Alpine"}, {"&SearchTerm=Beta", 1, "Beta"}, {"", 3, "Alpha"},
			} {
				t.Run(prefix+route+tc.query, func(t *testing.T) {
					status, raw := embyDo(t, ts, http.MethodGet, prefix+route+tc.query, token, "")
					var r struct {
						Items []struct{ Name string }
						Total int `json:"TotalRecordCount"`
					}
					if status != 200 || json.Unmarshal(raw, &r) != nil || r.Items == nil || r.Total != tc.total {
						t.Fatalf("%d %s want total=%d", status, raw, tc.total)
					}
					if tc.name == "" {
						if len(r.Items) != 0 {
							t.Fatalf("unexpected items: %s", raw)
						}
					} else if len(r.Items) != 1 || r.Items[0].Name != tc.name {
						t.Fatalf("%s want name=%s", raw, tc.name)
					}
				})
			}
		}
	}
}
