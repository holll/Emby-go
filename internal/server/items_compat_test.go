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

func TestBoxsetsRecursiveMovieCompatibility(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	first := libs[0]
	private, err := a.db.AddLibrary("Private collection library", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"A Alpha", "B Beta", "C Solo", "D Private", "E Uncollected"} {
		lib, collection := first, "Shared Set"
		if i == 2 {
			collection = "Solo Set"
		}
		if i == 3 {
			lib = private
		}
		if i == 4 {
			collection = ""
		}
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("compat-%d.strm", i)), Title: name, SortName: name, Collection: collection, Status: "success"}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 || i == 4 {
			if _, err := a.db.SetIndexedFavoriteDataForUser(1, id, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	type result struct {
		Items []map[string]any
		Total int `json:"TotalRecordCount"`
		Start int `json:"StartIndex"`
	}
	get := func(path, credential string) result {
		t.Helper()
		status, raw := embyDo(t, ts, http.MethodGet, path, credential, "")
		var r result
		if status != http.StatusOK || json.Unmarshal(raw, &r) != nil || r.Items == nil {
			t.Fatalf("%s: status=%d body=%s", path, status, raw)
		}
		return r
	}
	const types = "Movie,Series,MusicVideo,MusicAlbum"
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items", "/Users/1/Items"} {
			base := prefix + route + "?ParentId=boxsets&IncludeItemTypes=" + types
			for _, tc := range []struct {
				query string
				want  []string
				total int
				start int
			}{
				{"&Recursive=true", []string{"A Alpha", "B Beta", "C Solo", "D Private"}, 4, 0},
				{"&Recursive=true&SortBy=SortName&SortOrder=Descending", []string{"D Private", "C Solo", "B Beta", "A Alpha"}, 4, 0},
				{"&Recursive=true&SortOrder=Descending&StartIndex=1&Limit=2", []string{"C Solo", "B Beta"}, 4, 1},
				{"&Recursive=true&StartIndex=20&Limit=2", []string{}, 4, 20},
				{"&Recursive=true&Filters=IsFavorite", []string{"A Alpha"}, 1, 0},
				{"&Recursive=true&SearchTerm=Solo", []string{"C Solo"}, 1, 0},
				{"", []string{}, 0, 0},
				{"&Recursive=false", []string{}, 0, 0},
			} {
				r := get(base+tc.query, token)
				names, ids := make([]string, 0, len(r.Items)), map[string]bool{}
				for _, item := range r.Items {
					id := item["Id"].(string)
					if item["Type"] != "Movie" || ids[id] {
						t.Fatalf("wrong type or duplicate movie: %v", item)
					}
					ids[id] = true
					names = append(names, item["Name"].(string))
				}
				if !reflect.DeepEqual(names, tc.want) || r.Total != tc.total || r.Start != tc.start {
					t.Fatalf("%s: names=%v total=%d start=%d; want=%+v", base+tc.query, names, r.Total, r.Start, tc)
				}
			}
			// Querying members must not change explicit BoxSet browsing priority.
			for _, include := range []string{"BoxSet", "Movie,BoxSet"} {
				r := get(prefix+route+"?ParentId=boxsets&Recursive=true&IncludeItemTypes="+include, token)
				if r.Total != 1 || len(r.Items) != 1 || r.Items[0]["Type"] != "BoxSet" || r.Items[0]["Id"] != boxsetID("Shared Set") {
					t.Fatalf("BoxSet regression: %+v", r)
				}
			}
			for _, fields := range []string{"", "&Fields=MediaSources,Path,DateCreated", ""} {
				r := get(base+"&Recursive=true&Limit=1"+fields, token)
				if len(r.Items) != 1 {
					t.Fatalf("Fields query: %+v", r)
				}
				for _, key := range []string{"MediaSources", "Path", "DateCreated"} {
					if _, present := r.Items[0][key]; present != (fields != "") {
						t.Errorf("Fields %q key=%s item=%v", fields, key, r.Items[0])
					}
				}
			}
		}
	}
	// Visibility is per viewer, including a singleton set and zero grants.
	for _, granted := range []bool{true, false} {
		name, grants := fmt.Sprintf("compat-viewer-%t", granted), "[]"
		if granted {
			grants = "[" + strconv.FormatInt(first.ID, 10) + "]"
		}
		status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/users", token, fmt.Sprintf(`{"name":%q,"password":"visitor-pass-1","is_admin":false,"library_ids":%s}`, name, grants))
		if status != http.StatusCreated {
			t.Fatalf("create viewer: %d %s", status, raw)
		}
		status, raw = embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", fmt.Sprintf(`{"Username":%q,"Pw":"visitor-pass-1"}`, name))
		var auth struct {
			AccessToken string
			User        struct{ Id string }
		}
		if status != http.StatusOK || json.Unmarshal(raw, &auth) != nil || auth.AccessToken == "" {
			t.Fatalf("login: %d %s", status, raw)
		}
		for _, prefix := range []string{"", "/emby"} {
			for _, route := range []string{"/Items", "/Users/" + auth.User.Id + "/Items"} {
				base := prefix + route + "?ParentId=boxsets&Recursive=true&IncludeItemTypes=" + types
				r := get(base, auth.AccessToken)
				want := []string{}
				if granted {
					want = []string{"A Alpha", "B Beta", "C Solo"}
				}
				names := make([]string, 0, len(r.Items))
				for _, item := range r.Items {
					names = append(names, item["Name"].(string))
				}
				if !reflect.DeepEqual(names, want) || r.Total != len(want) {
					t.Fatalf("viewer granted=%t: %+v", granted, r)
				}
				favorite := get(base+"&Filters=IsFavorite", auth.AccessToken)
				if favorite.Total != 0 || len(favorite.Items) != 0 {
					t.Fatalf("admin favorites leaked to viewer: %+v", favorite)
				}
			}
			status, _ := embyDo(t, ts, http.MethodGet, prefix+"/Users/1/Items?ParentId=boxsets&Recursive=true&IncludeItemTypes="+types, auth.AccessToken, "")
			if status != http.StatusForbidden && status != http.StatusNotFound {
				t.Fatalf("cross-user query status=%d", status)
			}
		}
	}
}

func TestFolderFavoriteCompatibility(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		id, err := a.db.IndexMovie(store.Movie{LibraryID: libs[0].ID, SourcePath: filepath.Join(libs[0].Path, fmt.Sprintf("favorite-%d.strm", i)), Title: "Favorite movie", Collection: "Favorite Set", Status: "success"}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.SetIndexedFavoriteDataForUser(1, id, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items", "/Users/1/Items"} {
			for _, filter := range []string{"", "IsFavorite", "iSfAvOrItE", "IsUnplayed,IsFavorite"} {
				path := prefix + route + "?IncludeItemTypes=Folder&Recursive=true&Filters=" + url.QueryEscape(filter) + "&StartIndex=1&Limit=1"
				status, raw := embyDo(t, ts, http.MethodGet, path, token, "")
				var r struct {
					Items []map[string]any
					Total int `json:"TotalRecordCount"`
					Start int `json:"StartIndex"`
				}
				if status != http.StatusOK || json.Unmarshal(raw, &r) != nil || r.Items == nil || r.Start != 1 {
					t.Fatalf("%s: %d %s", path, status, raw)
				}
				if filter == "" {
					if r.Total != 2 || len(r.Items) != 1 || r.Items[0]["Type"] != "CollectionFolder" {
						t.Fatalf("ordinary folders changed: %+v", r)
					}
				} else if r.Total != 0 || len(r.Items) != 0 {
					t.Fatalf("favorite movies must not make their folders favorites: %+v", r)
				}
			}
		}
	}
}
