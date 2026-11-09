package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestFolderQueryContract(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	first := libs[0]
	second, err := a.db.AddLibrary("ZZ Folder Library", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		_, err := a.db.IndexMovie(store.Movie{LibraryID: first.ID, SourcePath: filepath.Join(first.Path, fmt.Sprintf("folder-%d.strm", i)), Title: "Folder movie", Collection: "Folder collection", Status: "success"}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
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
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		var r result
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.Items == nil {
			t.Fatalf("%s: Items must be an array: %s", path, raw)
		}
		return r
	}
	views := get("/Users/1/Views", token)
	if views.Total != 3 {
		t.Fatalf("want two libraries and collection view: %+v", views)
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items", "/Users/1/Items"} {
			base := prefix + route + "?IncludeItemTypes=Folder"
			for _, recursive := range []string{"", "&Recursive=false", "&Recursive=true"} {
				r := get(base+recursive+"&StartIndex=0&Limit=100", token)
				if r.Total != views.Total || len(r.Items) != views.Total || r.Start != 0 {
					t.Fatalf("%s: %+v", base+recursive, r)
				}
				for _, want := range views.Items {
					found := false
					for _, item := range r.Items {
						if item["Id"] == want["Id"] {
							found = true
							if !reflect.DeepEqual(item, want) || item["Type"] != "CollectionFolder" || item["IsFolder"] != true {
								t.Fatalf("Folder DTO differs from Views: got=%v want=%v", item, want)
							}
							status, raw := embyDo(t, ts, http.MethodGet, prefix+"/Items/"+item["Id"].(string), token, "")
							if status != http.StatusOK {
								t.Fatalf("folder detail not resolvable: %d %s", status, raw)
							}
						}
					}
					if !found {
						t.Fatalf("missing view %v in %+v", want, r)
					}
				}
			}
			all := get(base, token)
			page := get(base+"&StartIndex=1&Limit=1", token)
			if page.Total != all.Total || page.Start != 1 || !reflect.DeepEqual(page.Items, all.Items[1:2]) {
				t.Fatalf("pagination: %+v; all=%+v", page, all)
			}
			desc := get(base+"&SortBy=SortName&SortOrder=Descending", token)
			for i := range all.Items {
				if !reflect.DeepEqual(all.Items[i], desc.Items[len(desc.Items)-1-i]) {
					t.Fatalf("descending order: %+v", desc)
				}
			}
			filtered := get(base+"&SearchTerm=zz+folder", token)
			if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0]["Id"] != externalLibraryID(second.ID) {
				t.Fatalf("search: %+v", filtered)
			}
			for _, query := range []string{
				"&Ids=" + externalLibraryID(second.ID),
				"&Ids=" + externalLibraryID(second.ID) + ",unknown",
				"&Ids=unknown&Ids=" + externalLibraryID(second.ID),
				"&Ids=%20" + externalLibraryID(second.ID) + "%20",
			} {
				ids := get(base+query, token)
				if ids.Total != 1 || !reflect.DeepEqual(ids.Items, filtered.Items) {
					t.Fatalf("IDs filter %s: %+v", query, ids)
				}
			}
			normalized := get(base+"&StartIndex=-1&Limit=0", token)
			if normalized.Start != 0 || !reflect.DeepEqual(normalized.Items, all.Items) {
				t.Fatalf("pagination normalization: %+v", normalized)
			}
			root := get(base+"&ParentId=0", token)
			if !reflect.DeepEqual(root.Items, all.Items) {
				t.Fatalf("root alias: %+v", root)
			}
			for _, query := range []string{"&ExcludeItemTypes=Folder", "&ExcludeItemTypes=CollectionFolder", "&Ids=", "&Ids=unknown", "&SearchTerm=not-found", "&ParentId=" + externalLibraryID(first.ID), "&ParentId=" + externalLibraryID(second.ID), "&ParentId=boxsets", "&ParentId=" + url.QueryEscape(boxsetID("Folder collection")), "&ParentId=unknown"} {
				r := get(base+query, token)
				if r.Total != 0 || len(r.Items) != 0 {
					t.Fatalf("scoped/filter query %s: %+v", query, r)
				}
			}
			past := get(base+"&StartIndex=100", token)
			if past.Total != all.Total || past.Start != 100 || len(past.Items) != 0 {
				t.Fatalf("past end: %+v", past)
			}
			movies := get(prefix+route+"?IncludeItemTypes=Folder,Movie", token)
			if movies.Total != 2 || movies.Items[0]["Type"] != "Movie" {
				t.Fatalf("mixed type regression: %+v", movies)
			}
			unsupported := get(prefix+route+"?IncludeItemTypes=Series", token)
			if unsupported.Total != 0 {
				t.Fatalf("unsupported type regression: %+v", unsupported)
			}
		}
	}

	// A user with one grant sees only that library; zero grants must not expose
	// even the synthetic collection view. User-ID routes cannot bypass auth.
	for _, granted := range []bool{true, false} {
		name := fmt.Sprintf("folder-viewer-%t", granted)
		grants := "[]"
		if granted {
			grants = fmt.Sprintf("[%d]", second.ID)
		}
		status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/users", token, fmt.Sprintf(`{"name":%q,"password":"visitor-pass-1","is_admin":false,"library_ids":%s}`, name, grants))
		if status != http.StatusCreated {
			t.Fatalf("create: %d %s", status, raw)
		}
		status, raw = embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", fmt.Sprintf(`{"Username":%q,"Pw":"visitor-pass-1"}`, name))
		var auth struct {
			AccessToken string
			User        struct {
				ID string `json:"Id"`
			}
		}
		if status != http.StatusOK || json.Unmarshal(raw, &auth) != nil || auth.AccessToken == "" {
			t.Fatalf("login: %d %s", status, raw)
		}
		for _, prefix := range []string{"", "/emby"} {
			for _, route := range []string{"/Items", "/Users/" + auth.User.ID + "/Items"} {
				r := get(prefix+route+"?IncludeItemTypes=Folder&Recursive=true", auth.AccessToken)
				if granted {
					if r.Total != 1 || len(r.Items) != 1 || r.Items[0]["Id"] != externalLibraryID(second.ID) {
						t.Fatalf("ACL: %+v", r)
					}
				} else if r.Total != 0 || len(r.Items) != 0 {
					t.Fatalf("no grants: %+v", r)
				}
			}
			status, _ = embyDo(t, ts, http.MethodGet, prefix+"/Users/1/Items?IncludeItemTypes=Folder", auth.AccessToken, "")
			if status != http.StatusForbidden && status != http.StatusNotFound {
				t.Fatalf("cross-user status=%d", status)
			}
		}
	}
	for _, path := range []string{"/Items", "/emby/Items", "/Users/1/Items", "/emby/Users/1/Items"} {
		status, _ := embyDo(t, ts, http.MethodGet, path+"?IncludeItemTypes=Folder", "", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d", path, status)
		}
	}
}
