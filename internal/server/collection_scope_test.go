package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/store"
)

// Exercise the actual authenticated /emby routes with multiple unrelated sets;
// a one-set fixture cannot detect an accidentally unscoped ownership response.
func TestCollectionScopeAndTypeContract(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	lib := libs[0]
	var movieID int64
	for i, name := range []string{"Alpha", "Alpha", "Beta", "Beta", ""} {
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("scope-%d.strm", i)), Title: fmt.Sprintf("Scope %d", i), Collection: name, Status: "success"}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			movieID = id
		}
	}
	type item struct {
		ID            string   `json:"Id"`
		Type          string   `json:"Type"`
		CollectionIDs []string `json:"CollectionIds"`
	}
	type result struct {
		Items []item
		Total int `json:"TotalRecordCount"`
	}
	get := func(path string) result {
		t.Helper()
		status, raw := embyDo(t, ts, http.MethodGet, "/emby"+path, token, "")
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		var r result
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, prefix := range []string{"/Items/", "/Users/1/Items/"} {
		r := get(prefix + strconv.FormatInt(movieID, 10) + "/Collections")
		if len(r.Items) != 1 || r.Items[0].ID != boxsetID("Alpha") {
			t.Errorf("ownership %s: %+v", prefix, r)
		}
	}
	// Model a client resolving the film's CollectionIds via GET /Items?Ids=... .
	// Ignoring this filter returns unrelated BoxSets despite a single requested ID.
	for _, prefix := range []string{"/Items", "/Users/1/Items"} {
		r := get(prefix + "?IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(boxsetID("Alpha")))
		if len(r.Items) != 1 || r.Items[0].ID != boxsetID("Alpha") {
			t.Errorf("specific collection lookup %s: %+v", prefix, r)
		}
		for _, ids := range []string{"999999", "unknown", ""} {
			r := get(prefix + "?IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(ids))
			if len(r.Items) != 0 {
				t.Errorf("non-collection/empty Ids=%q: %+v", ids, r)
			}
		}
	}
	for _, query := range []string{
		"IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(boxsetID("Alpha")+","+boxsetID("Alpha")),
		"IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(boxsetID("Alpha")) + "&Ids=" + url.QueryEscape(boxsetID("Alpha")),
		"IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(" "+boxsetID("Alpha")+" ") + "&Limit=1",
	} {
		r := get("/Items?" + query)
		if len(r.Items) != 1 || r.Total != 1 || r.Items[0].ID != boxsetID("Alpha") {
			t.Errorf("repeated/trimmed IDs %s: %+v", query, r)
		}
	}
	paged := get("/Items?IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(boxsetID("Alpha")) + "&StartIndex=1&Limit=1")
	if len(paged.Items) != 0 || paged.Total != 1 {
		t.Errorf("filter must precede paging: %+v", paged)
	}
	both := get("/Items?IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(boxsetID("Alpha")+","+boxsetID("Beta")))
	if len(both.Items) != 2 || both.Total != 2 {
		t.Errorf("explicit two IDs: %+v", both)
	}
	for _, query := range []string{"IncludeItemTypes=BoxSet&ExcludeItemTypes=BoxSet", "ParentId=boxsets&ExcludeItemTypes=BoxSet", "IncludeItemTypes=BoxSet&Ids=" + url.QueryEscape(boxsetID("Alpha")) + "&SearchTerm=Beta"} {
		r := get("/Items?" + query)
		if len(r.Items) != 0 {
			t.Errorf("collection filter %s: %+v", query, r)
		}
	}
	for _, tc := range []struct {
		parent, types string
		count         int
		kind          string
	}{
		{"", "BoxSet", 2, "BoxSet"}, {"0", "BoxSet", 2, "BoxSet"},
		{"boxsets", "BoxSet", 2, "BoxSet"}, {"boxsets", "Movie", 0, ""}, {"boxsets", "Series", 0, ""},
		{boxsetID("Alpha"), "Movie", 2, "Movie"}, {boxsetID("Alpha"), "", 2, "Movie"},
		{boxsetID("Alpha"), "BoxSet", 0, ""}, {boxsetID("Alpha"), "Series", 0, ""},
		{"unknown-parent", "BoxSet", 0, ""}, {"unknown-parent", "Movie", 0, ""},
		{strconv.FormatInt(movieID, 10), "BoxSet", 0, ""},
		{externalLibraryID(lib.ID), "BoxSet", 2, "BoxSet"},
	} {
		t.Run(tc.parent+"/"+tc.types, func(t *testing.T) {
			r := get("/Users/1/Items?ParentId=" + url.QueryEscape(tc.parent) + "&IncludeItemTypes=" + tc.types)
			if r.Total != tc.count || len(r.Items) != tc.count {
				t.Fatalf("got %+v want %d", r, tc.count)
			}
			for _, it := range r.Items {
				if it.Type != tc.kind {
					t.Errorf("wrong type %+v", it)
				}
			}
		})
	}
}

func TestCollectionListHonorsLibraryGrants(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.db.AddLibrary("Hidden", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range []store.Library{libs[0], other} {
		name := "Visible"
		if lib.ID == other.ID {
			name = "Private"
		}
		for i := 0; i < 2; i++ {
			_, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("scope-%d.strm", i)), Title: name, Collection: name, Status: "success"}, 1, time.Unix(1, 0), nil)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/users", token, `{"name":"scope-viewer","password":"visitor-pass-1","is_admin":false,"library_ids":[`+strconv.FormatInt(libs[0].ID, 10)+`]}`)
	if status != http.StatusCreated {
		t.Fatalf("create viewer %d %s", status, raw)
	}
	status, raw = embyDo(t, ts, http.MethodPost, "/emby/Users/AuthenticateByName", "", `{"Username":"scope-viewer","Pw":"visitor-pass-1"}`)
	var auth struct{ AccessToken string }
	if status != http.StatusOK || json.Unmarshal(raw, &auth) != nil || auth.AccessToken == "" {
		t.Fatalf("login %d %s", status, raw)
	}
	for _, path := range []string{"/Items?IncludeItemTypes=BoxSet", "/Items?ParentId=boxsets&IncludeItemTypes=BoxSet"} {
		status, raw = embyDo(t, ts, http.MethodGet, "/emby"+path, auth.AccessToken, "")
		var r struct {
			Items []struct {
				ID string `json:"Id"`
			}
		}
		if status != http.StatusOK || json.Unmarshal(raw, &r) != nil || len(r.Items) != 1 || r.Items[0].ID != boxsetID("Visible") {
			t.Errorf("%s: %d %s", path, status, raw)
		}
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/emby/Items/"+boxsetID("Private"), auth.AccessToken, "")
	if status != http.StatusNotFound {
		t.Errorf("private boxset detail: %d %s", status, raw)
	}
}
