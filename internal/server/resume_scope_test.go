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

func TestResumeParentScopeCompatibility(t *testing.T) {
	a, ts, adminToken := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	public := libs[0]
	private, err := a.db.AddLibrary("Private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	viewer := seedFixtureUser(t, a, "resume-viewer", "password-1234", false)
	if err := updateFixtureUser(t, a, viewer.ID, store.AdminUserUpdate{LibraryIDs: fixtureValue([]int64{public.ID})}); err != nil {
		t.Fatal(err)
	}
	const token = "resume-scope-viewer"
	if err := saveFixtureSession(t, a, token, viewer.ID, a.sessionExpiry(time.Now())); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"Alpha", "Beta", "Solo", "Private", "Uncollected", "Hidden", "NoProgress", "OtherUser"} {
		lib, collection := public, "Pair"
		if name == "Private" {
			lib = private
		}
		if name == "Solo" {
			collection = "Solo"
		}
		if name == "Uncollected" {
			collection = ""
		}
		id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("resume-scope-%d.strm", i)), Title: name, Collection: collection, Status: "success", RuntimeSeconds: 1000}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, uid := range []int64{1, viewer.ID} {
			if name == "NoProgress" || name == "OtherUser" && uid == viewer.ID {
				continue
			}
			if _, err := a.db.RecordPlaybackEvent(store.PlaybackEvent{SessionKey: fmt.Sprintf("resume-%d-%d", uid, id), UserID: uid, MovieID: id, Kind: store.PlaybackStopped, PositionTicks: 10000000, Now: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if name == "Hidden" {
				if _, err := a.db.SetIndexedHideFromResumeDataForUser(uid, id, true); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, tc := range []struct {
			parent              string
			start, limit, total int
			want                []string
		}{
			{"", 0, 30, 4, []string{"Alpha", "Beta", "Solo", "Uncollected"}},
			{"0", 0, 30, 4, []string{"Alpha", "Beta", "Solo", "Uncollected"}},
			{externalLibraryID(public.ID), 0, 30, 4, []string{"Alpha", "Beta", "Solo", "Uncollected"}},
			{strconv.FormatInt(public.ID, 10), 1, 1, 4, []string{"Beta"}},
			{boxsetID("Pair"), 0, 1, 2, []string{"Alpha"}},
			{boxsetID("Pair"), 1, 1, 2, []string{"Beta"}},
			{boxsetID("Solo"), 0, 30, 1, []string{"Solo"}},
			{"boxsets", 1, 1, 3, []string{"Beta"}},
			{boxsetID("Pair"), 10, 1, 2, []string{}},
			{"unknown", 5, 1, 0, []string{}},
			{externalLibraryID(private.ID + 1), 0, 30, 0, []string{}},
			{externalLibraryID(private.ID), 0, 30, 0, []string{}},
			{boxsetID("missing"), 0, 30, 0, []string{}},
			{"unknown", 0, 30, 0, []string{}}, {"-1", 0, 30, 0, []string{}},
			{strconv.FormatInt(libraryIDBase, 10), 0, 30, 0, []string{}}, {"boxset:", 0, 30, 0, []string{}},
		} {
			t.Run(fmt.Sprintf("%s/%s/%d", prefix, tc.parent, tc.start), func(t *testing.T) {
				path := fmt.Sprintf("%s/Users/%d/Items/Resume?ParentId=%s&StartIndex=%d&Limit=%d&Fields=DateCreated", prefix, viewer.ID, url.QueryEscape(tc.parent), tc.start, tc.limit)
				status, raw := embyDo(t, ts, http.MethodGet, path, token, "")
				var r struct {
					Items []struct {
						Name, DateCreated string
						UserData          struct{ PlaybackPositionTicks int64 }
					}
					Total int `json:"TotalRecordCount"`
					Start int `json:"StartIndex"`
				}
				if status != 200 || json.Unmarshal(raw, &r) != nil || r.Items == nil {
					t.Fatalf("%d %s", status, raw)
				}
				names := make([]string, 0, len(r.Items))
				for _, item := range r.Items {
					names = append(names, item.Name)
					if item.DateCreated == "" || item.UserData.PlaybackPositionTicks != 10000000 {
						t.Fatalf("lost Fields/progress: %s", raw)
					}
				}
				if !reflect.DeepEqual(names, tc.want) || r.Total != tc.total || r.Start != tc.start {
					t.Fatalf("got=%v total=%d start=%d want=%v total=%d start=%d", names, r.Total, r.Start, tc.want, tc.total, tc.start)
				}
			})
		}
		status, raw := embyDo(t, ts, http.MethodGet, prefix+"/Users/1/Items/Resume?ParentId="+url.QueryEscape(externalLibraryID(private.ID)), adminToken, "")
		var admin struct {
			Items []struct{ Name string }
			Total int `json:"TotalRecordCount"`
		}
		if status != 200 || json.Unmarshal(raw, &admin) != nil || admin.Total != 1 || len(admin.Items) != 1 || admin.Items[0].Name != "Private" {
			t.Fatalf("admin private scope: %d %s", status, raw)
		}
		status, _ = embyDo(t, ts, http.MethodGet, prefix+"/Users/1/Items/Resume", token, "")
		if status != http.StatusNotFound {
			t.Fatalf("cross-user: %d", status)
		}
	}
	if err := updateFixtureUser(t, a, viewer.ID, store.AdminUserUpdate{LibraryIDs: fixtureValue([]int64{})}); err != nil {
		t.Fatal(err)
	}
	status, raw := embyDo(t, ts, http.MethodGet, fmt.Sprintf("/Users/%d/Items/Resume?ParentId=%s", viewer.ID, url.QueryEscape(boxsetID("Pair"))), token, "")
	var empty struct {
		Items []any
		Total int `json:"TotalRecordCount"`
	}
	if status != 200 || json.Unmarshal(raw, &empty) != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Total != 0 {
		t.Fatalf("no grants: %d %s", status, raw)
	}
}
