package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestCollectionListItemIds(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	lib := libs[0]
	hidden, err := a.db.AddLibrary("Hidden members", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	add := func(l store.Library, name, status string) int64 {
		t.Helper()
		id, err := a.db.IndexMovie(store.Movie{LibraryID: l.ID, SourcePath: filepath.Join(l.Path, fmt.Sprintf("member-%s-%s-%d.strm", name, status, time.Now().UnixNano())), Title: name, Collection: name, Status: status}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	alpha := add(lib, "Alpha", "success")
	add(lib, "Alpha", "success")
	beta := add(lib, "Beta", "manual")
	add(lib, "Beta", "success")
	none := add(lib, "", "success")
	pending := add(lib, "Alpha", "pending")
	solo := add(lib, "Solo", "success")
	hiddenAlpha := add(hidden, "Alpha", "success")
	add(hidden, "Alpha", "success")
	private := add(hidden, "Private", "success")
	add(hidden, "Private", "success")
	type item struct {
		ID    string `json:"Id"`
		Count int    `json:"ChildCount"`
	}
	type result struct {
		Items []item
		Total int `json:"TotalRecordCount"`
	}
	get := func(path, auth string) result {
		t.Helper()
		status, raw := embyDo(t, ts, http.MethodGet, "/emby"+path, auth, "")
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		var r result
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	member := func(id int64) string { return strconv.FormatInt(id, 10) }
	for _, prefix := range []string{"/Items", "/Users/1/Items"} {
		for _, tc := range []struct {
			name, query string
			names       []string
			total       int
		}{
			{"single", "ListItemIds=" + member(alpha), []string{"Alpha"}, 1},
			{"baseline", "", []string{"Alpha", "Beta", "Private"}, 3},
			{"multiple", "ListItemIds=" + member(alpha) + "," + member(beta), []string{"Alpha", "Beta"}, 2},
			{"repeated", "ListItemIds=" + member(alpha) + "," + member(alpha) + "&ListItemIds=" + member(alpha), []string{"Alpha"}, 1},
			{"trimmed", "ListItemIds=" + url.QueryEscape(" "+member(alpha)+" "), []string{"Alpha"}, 1},
			{"mixed", "ListItemIds=invalid," + member(alpha), []string{"Alpha"}, 1},
			{"none", "ListItemIds=" + member(none), nil, 0},
			{"pending", "ListItemIds=" + member(pending), nil, 0},
			{"threshold", "ListItemIds=" + member(solo), nil, 0},
			{"empty", "ListItemIds=", nil, 0},
			{"bare", "ListItemIds", nil, 0},
			{"invalid", "ListItemIds=invalid,-1,0,999999999,9223372036854775808", nil, 0},
			{"ids-intersection", "ListItemIds=" + member(alpha) + "&Ids=" + url.QueryEscape(boxsetID("Beta")), nil, 0},
			{"search-intersection", "ListItemIds=" + member(alpha) + "&SearchTerm=Beta", nil, 0},
			{"exclude", "ListItemIds=" + member(alpha) + "&ExcludeItemTypes=BoxSet", nil, 0},
			{"parent", "ListItemIds=" + member(alpha) + "&ParentId=boxsets", []string{"Alpha"}, 1},
			{"other-library", "ListItemIds=" + member(hiddenAlpha) + "&ParentId=" + externalLibraryID(lib.ID), nil, 0},
			{"paging", "ListItemIds=" + member(alpha) + "&StartIndex=1&Limit=1", nil, 1},
		} {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				r := get(prefix+"?IncludeItemTypes=BoxSet&"+tc.query, token)
				if len(r.Items) != len(tc.names) || r.Total != tc.total {
					t.Fatalf("got %+v want names=%v total=%d", r, tc.names, tc.total)
				}
				for i, name := range tc.names {
					if r.Items[i].ID != boxsetID(name) {
						t.Fatalf("wrong collection: %+v", r)
					}
					wantCount := 2
					if name == "Alpha" {
						wantCount = 4
					}
					if r.Items[i].Count != wantCount {
						t.Fatalf("full collection count lost: %+v", r)
					}
				}
			})
		}
	}
	// More than one SQL batch; valid members must not disappear at the boundary.
	ids := make([]string, 600)
	for i := range ids {
		ids[i] = strconv.Itoa(100000 + i)
	}
	ids = append(ids, member(alpha))
	if r := get("/Items?IncludeItemTypes=BoxSet&ListItemIds="+strings.Join(ids, ","), token); r.Total != 1 || len(r.Items) != 1 || r.Items[0].ID != boxsetID("Alpha") {
		t.Fatalf("batched lookup: %+v", r)
	}
	if err := a.db.SetSetting(settingCollectionMinMovies, "1"); err != nil {
		t.Fatal(err)
	}
	if r := get("/Items?IncludeItemTypes=BoxSet&ListItemIds="+member(solo), token); r.Total != 1 || len(r.Items) != 1 || r.Items[0].Count != 1 {
		t.Fatalf("threshold one: %+v", r)
	}
	status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/users", token, `{"name":"member-viewer","password":"visitor-pass-1","is_admin":false,"library_ids":[`+member(lib.ID)+`]}`)
	if status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", status, raw)
	}
	status, raw = embyDo(t, ts, http.MethodPost, "/emby/Users/AuthenticateByName", "", `{"Username":"member-viewer","Pw":"visitor-pass-1"}`)
	var auth struct {
		AccessToken string
		User        struct {
			ID string `json:"Id"`
		}
	}
	if status != http.StatusOK || json.Unmarshal(raw, &auth) != nil || auth.AccessToken == "" {
		t.Fatalf("login: %d %s", status, raw)
	}
	for _, prefix := range []string{"/Items", "/Users/" + auth.User.ID + "/Items"} {
		for _, id := range []int64{hiddenAlpha, private} {
			r := get(prefix+"?IncludeItemTypes=BoxSet&ListItemIds="+member(id), auth.AccessToken)
			if r.Total != 0 || len(r.Items) != 0 {
				t.Fatalf("hidden member disclosed ownership: %+v", r)
			}
		}
		r := get(prefix+"?IncludeItemTypes=BoxSet&ListItemIds="+member(alpha)+","+member(private), auth.AccessToken)
		if r.Total != 1 || len(r.Items) != 1 || r.Items[0].ID != boxsetID("Alpha") || r.Items[0].Count != 2 {
			t.Fatalf("visible full stats: %+v", r)
		}
	}
}
