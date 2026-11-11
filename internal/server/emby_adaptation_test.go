package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestLatestGroupedBoxsetsCompatibility(t *testing.T) {
	a, ts, admin := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	private, err := a.db.AddLibrary("private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate newest members must not consume Limit before collection grouping.
	for i, row := range []struct {
		name    string
		library int64
		status  string
	}{
		{"Alpha", libs[0].ID, "success"}, {"Alpha", libs[0].ID, "success"},
		{"Beta", libs[0].ID, "success"}, {"Beta", libs[0].ID, "manual"}, {"Beta", libs[0].ID, "success"},
		{"Solo", libs[0].ID, "success"}, {"Private", private.ID, "success"}, {"Private", private.ID, "success"},
		{"Alpha", private.ID, "success"}, {"Alpha", libs[0].ID, "pending"},
	} {
		_, err := a.db.IndexMovie(store.Movie{LibraryID: row.library, SourcePath: filepath.Join(t.TempDir(), fmt.Sprintf("%d.strm", i)), Title: fmt.Sprintf("member-%d", i), Collection: row.name, Status: row.status}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	viewer := seedFixtureUser(t, a, "grouped-viewer", "password-1234", false)
	if err := updateFixtureUser(t, a, viewer.ID, store.AdminUserUpdate{LibraryIDs: fixtureValue([]int64{libs[0].ID})}); err != nil {
		t.Fatal(err)
	}
	const viewerToken = "grouped-viewer-token"
	if err := saveFixtureSession(t, a, viewerToken, viewer.ID, a.sessionExpiry(time.Now())); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, tc := range []struct {
			uid, token string
			limit      int
			names      []string
			counts     []int
		}{
			{"1", admin, 10, []string{"Alpha", "Private", "Beta"}, []int{3, 2, 3}},
			{"1", admin, 2, []string{"Alpha", "Private"}, []int{3, 2}},
			{strconv.FormatInt(viewer.ID, 10), viewerToken, 10, []string{"Beta", "Alpha"}, []int{3, 2}},
			{strconv.FormatInt(viewer.ID, 10), viewerToken, 1, []string{"Beta"}, []int{3}},
		} {
			path := fmt.Sprintf("%s/Users/%s/Items/Latest?ParentId=boxsets&GroupItems=true&Limit=%d", prefix, tc.uid, tc.limit)
			status, raw := embyDo(t, ts, http.MethodGet, path, tc.token, "")
			var items []struct {
				ID         string `json:"Id"`
				Name, Type string
				IsFolder   bool
				ChildCount int
			}
			if status != 200 || json.Unmarshal(raw, &items) != nil || items == nil {
				t.Fatalf("%s: %d %s", path, status, raw)
			}
			if len(items) != len(tc.names) {
				t.Fatalf("%s: %s", path, raw)
			}
			for i, item := range items {
				if item.ID != boxsetID(tc.names[i]) || item.Name != tc.names[i] || item.Type != "BoxSet" || !item.IsFolder || item.ChildCount != tc.counts[i] {
					t.Fatalf("%s: wrong DTO %s", path, raw)
				}
			}
		}
		for _, group := range []string{"", "&GroupItems=false"} {
			status, raw := embyDo(t, ts, http.MethodGet, prefix+"/Users/1/Items/Latest?ParentId=boxsets&Limit=100"+group, admin, "")
			var items []struct {
				Type     string
				IsFolder bool
			}
			if status != 200 || json.Unmarshal(raw, &items) != nil || len(items) != 9 {
				t.Fatalf("legacy members: %d %s", status, raw)
			}
			for _, item := range items {
				if item.Type != "Movie" || item.IsFolder {
					t.Fatalf("legacy DTO: %s", raw)
				}
			}
		}
		status, raw := embyDo(t, ts, http.MethodGet, prefix+"/Users/1/Items/Latest?ParentId=boxsets&GroupItems=invalid", admin, "")
		if status != 400 {
			t.Fatalf("invalid GroupItems: %d %s", status, raw)
		}
	}
}

func TestEmbyAdaptationQueryFieldsAndPlaybackUsers(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, row := range []struct {
		title, premiere string
		backdrop        bool
	}{{"A", "2024-01-01", false}, {"Z", "2020-01-01", true}, {"M", "2022-01-01", true}} {
		movie := store.Movie{LibraryID: libs[0].ID, SourcePath: filepath.Join(libs[0].Path, fmt.Sprintf("adapt-%d.strm", i)), Title: row.title, Premiere: row.premiere, Collection: "shared", Status: "success", ProviderID: "metatube-only", SourceProtocol: "https"}
		if err := os.WriteFile(movie.SourcePath, []byte("https://example.com/video.mkv"), 0o644); err != nil {
			t.Fatal(err)
		}
		if row.backdrop {
			movie.ExtraBackdrops = []string{"/indexed/extra.jpg"}
		}
		id, err := a.db.IndexMovie(movie, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items", "/Users/1/Items"} {
			base := prefix + route + "?IncludeItemTypes=Movie&ParentId=" + externalLibraryID(libs[0].ID)
			for _, tc := range []struct {
				query string
				names []string
				total int
			}{
				{"&SortBy=PremiereDate", []string{"Z", "M", "A"}, 3},
				{"&ImageTypes=Backdrop&SortBy=PremiereDate&Limit=1", []string{"Z"}, 2},
				{"&ImageTypes=Backdrop&SortBy=PremiereDate&Limit=1&StartIndex=1", []string{"M"}, 2},
				{"&HasTmdbId=true", []string{}, 0},
				{"&HasTmdbId=false&ImageTypes=Backdrop&SortBy=PremiereDate", []string{"Z", "M"}, 2},
			} {
				status, raw := embyDo(t, ts, http.MethodGet, base+tc.query, token, "")
				var result struct {
					Items            []struct{ Name string }
					TotalRecordCount int
				}
				if status != 200 || json.Unmarshal(raw, &result) != nil || result.Items == nil {
					t.Fatalf("query %s: %d %s", tc.query, status, raw)
				}
				names := make([]string, 0, len(result.Items))
				for _, item := range result.Items {
					names = append(names, item.Name)
				}
				if !reflect.DeepEqual(names, tc.names) || result.TotalRecordCount != tc.total {
					t.Fatalf("query %s: %s want=%v/%d", tc.query, raw, tc.names, tc.total)
				}
			}
			status, raw := embyDo(t, ts, http.MethodGet, base+"&HasTmdbId=invalid", token, "")
			if status != 400 {
				t.Fatalf("bad HasTmdbId: %d %s", status, raw)
			}
			// Toggle response options against the same cache entry, then verify restoration.
			for _, enabled := range []bool{false, true, false, true} {
				status, raw := embyDo(t, ts, http.MethodGet, base+fmt.Sprintf("&EnableUserData=%t&EnableTotalRecordCount=%t", enabled, enabled), token, "")
				var result map[string]any
				if status != 200 || json.Unmarshal(raw, &result) != nil {
					t.Fatalf("output switches: %d %s", status, raw)
				}
				_, total := result["TotalRecordCount"]
				if total != enabled {
					t.Fatalf("total switch: %s", raw)
				}
				for _, value := range result["Items"].([]any) {
					_, data := value.(map[string]any)["UserData"]
					if data != enabled {
						t.Fatalf("userdata switch: %s", raw)
					}
				}
			}
			for _, key := range []string{"EnableUserData", "EnableTotalRecordCount"} {
				status, raw := embyDo(t, ts, http.MethodGet, base+"&"+key+"=invalid", token, "")
				if status != 400 {
					t.Fatalf("bad %s: %d %s", key, status, raw)
				}
			}
		}
		source := prefix + "/Items/" + strconv.FormatInt(ids[0], 10)
		for _, fields := range []string{"DateCreated,Path,Size,MediaStreams,MediaSources", "", "DateCreated,Path,Size,MediaStreams,MediaSources"} {
			status, raw := embyDo(t, ts, http.MethodGet, source+"/Similar?Limit=10&Fields="+url.QueryEscape(fields), token, "")
			var result struct{ Items []map[string]any }
			if status != 200 || json.Unmarshal(raw, &result) != nil || len(result.Items) != 2 {
				t.Fatalf("Similar fields: %d %s", status, raw)
			}
			for _, item := range result.Items {
				if _, present := item["Size"]; present {
					t.Fatalf("Size without probe data must not be fabricated: %s", raw)
				}
				for _, key := range []string{"DateCreated", "Path", "MediaStreams", "MediaSources"} {
					_, present := item[key]
					if present != (fields != "") {
						t.Fatalf("Similar %s fields=%q: %s", key, fields, raw)
					}
				}
			}
		}
		for _, endpoint := range []string{"/Similar", "/PlaybackInfo"} {
			for _, uid := range []string{"1", "999999", "invalid"} {
				status, raw := embyDo(t, ts, http.MethodGet, source+endpoint+"?UserId="+uid, token, "")
				want := 404
				if uid == "1" {
					want = 200
				}
				if status != want {
					t.Fatalf("user %s %s: %d %s", uid, endpoint, status, raw)
				}
			}
		}
		for _, tc := range []struct {
			body   string
			status int
		}{{"", 200}, {`{}`, 200}, {`{"UserId":"1","DeviceProfile":{}}`, 200}, {`{"UserId":"999999"}`, 404}, {`{"UserId":"bad"}`, 404}, {`{"UserId":1}`, 400}, {`{`, 400}, {`{} {}`, 400}} {
			status, raw := embyDo(t, ts, http.MethodPost, source+"/PlaybackInfo", token, tc.body)
			if status != tc.status {
				t.Fatalf("Playback body %q: %d %s", tc.body, status, raw)
			}
		}
	}
}
