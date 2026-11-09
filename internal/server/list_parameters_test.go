package server

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestListFieldsAndImageParameters(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	root := libs[0].Path
	poster := filepath.Join(root, "compat-poster.png")
	f, err := os.Create(poster)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 40, 20))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(root, "compat.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><fileinfo><size>12345</size><streamdetails><video><codec>h264</codec><width>1920</width><height>1080</height></video></streamdetails></fileinfo></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := a.db.IndexMovie(store.Movie{LibraryID: libs[0].ID, SourcePath: filepath.Join(root, "compat.strm"), Title: "Compat", Status: "success", PosterPath: poster, LandscapePath: poster, BackdropPath: poster, ExtraBackdrops: []string{poster, poster}, NFOPath: nfoPath, RuntimeSeconds: 1000}, 1, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.RecordPlaybackEvent(store.PlaybackEvent{SessionKey: "compat", UserID: 1, MovieID: id, Kind: store.PlaybackStopped, PositionTicks: 10000000, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) map[string]any {
		t.Helper()
		status, raw := embyDo(t, ts, http.MethodGet, path, token, "")
		if status != 200 {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		var entries []any
		switch root := payload.(type) {
		case map[string]any:
			var ok bool
			entries, ok = root["Items"].([]any)
			if !ok {
				return root
			}
		case []any:
			entries = root
		}
		if len(entries) != 1 {
			t.Fatalf("%s: %s", path, raw)
		}
		return entries[0].(map[string]any)
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items?IncludeItemTypes=Movie", "/Users/1/Items?IncludeItemTypes=Movie", "/Users/1/Items/Resume?Limit=30", "/Users/1/Items/Latest?Limit=30"} {
			base := prefix + route
			for _, fields := range []string{"", "MediaSources,DateCreated,Path,Size,MediaStreams,PrimaryImageAspectRatio", ""} {
				item := get(base + "&Fields=" + fields)
				for _, key := range []string{"MediaSources", "DateCreated", "Path", "Size", "MediaStreams", "PrimaryImageAspectRatio"} {
					if _, ok := item[key]; ok != (fields != "") {
						t.Fatalf("%s Fields=%q key=%s item=%v", base, fields, key, item)
					}
				}
				if fields != "" && (item["Size"] != float64(12345) || item["PrimaryImageAspectRatio"] != float64(2)) {
					t.Fatalf("wrong optional metadata: %v", item)
				}
			}
			item := get(base + "&Fields=Size,MediaStreams")
			if item["Size"] != float64(12345) || item["MediaStreams"] == nil || item["MediaSources"] != nil {
				t.Fatalf("independent optional fields: %v", item)
			}
		}
		for _, base := range []string{prefix + "/Items?IncludeItemTypes=Movie", prefix + "/Users/1/Items/Resume?Limit=30", prefix + "/Users/1/Items/Latest?Limit=30", fmt.Sprintf("%s/Items/%d?x=1", prefix, id)} {
			full := get(base)
			if len(full["BackdropImageTags"].([]any)) != 3 {
				t.Fatalf("fixture images: %v", full)
			}
			limited := get(base + "&ImageTypeLimit=1")
			tags := limited["ImageTags"].(map[string]any)
			backs := limited["BackdropImageTags"].([]any)
			if len(backs) != 1 || tags["Backdrop"] != backs[0] {
				t.Fatalf("image limit/sync: %v", limited)
			}
			primary := get(base + "&EnableImageTypes=pRiMaRy&ImageTypeLimit=1")
			if len(primary["ImageTags"].(map[string]any)) != 1 || primary["ImageTags"].(map[string]any)["Primary"] == nil || len(primary["BackdropImageTags"].([]any)) != 0 {
				t.Fatalf("image type filter: %v", primary)
			}
			zero := get(base + "&ImageTypeLimit=0")
			if len(zero["ImageTags"].(map[string]any)) != 0 || len(zero["BackdropImageTags"].([]any)) != 0 {
				t.Fatalf("zero images: %v", zero)
			}
			if again := get(base); !reflect.DeepEqual(full["ImageTags"], again["ImageTags"]) || !reflect.DeepEqual(full["BackdropImageTags"], again["BackdropImageTags"]) {
				t.Fatalf("filtered image response polluted cache: %v", again)
			}
			for _, value := range []string{"-1", "bad", "", "9999999999999999999999999"} {
				status, raw := embyDo(t, ts, http.MethodGet, base+"&ImageTypeLimit="+value, token, "")
				if status != http.StatusBadRequest || !strings.Contains(string(raw), "ImageTypeLimit") {
					t.Fatalf("invalid limit %q: %d %s", value, status, raw)
				}
			}
		}
	}
}

func TestBoxsetAndEntityDescendingPagination(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"Alpha", "Zulu"} {
		for j := range 2 {
			if _, err := a.db.IndexMovie(store.Movie{LibraryID: libs[0].ID, SourcePath: filepath.Join(libs[0].Path, fmt.Sprintf("set-%d-%d.strm", i, j)), Title: name, Collection: name, Genres: []string{name}, Tags: []string{name}, Studios: []string{name}, Status: "success"}, 1, time.Unix(1, 0), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, prefix := range []string{"", "/emby"} {
		for _, route := range []string{"/Items?IncludeItemTypes=BoxSet", "/Users/1/Items?IncludeItemTypes=BoxSet", "/Genres?x=1", "/Items?IncludeItemTypes=Genre", "/Items?IncludeItemTypes=Tag", "/Items?IncludeItemTypes=Studio"} {
			for _, tc := range []struct {
				order string
				start int
				want  string
			}{{"Ascending", 0, "Alpha"}, {"Descending", 0, "Zulu"}, {"Descending", 1, "Alpha"}, {"Ascending", 1, "Zulu"}} {
				path := fmt.Sprintf("%s%s&ParentId=boxsets&SortBy=SortName&SortOrder=%s&StartIndex=%d&Limit=1", prefix, route, tc.order, tc.start)
				status, raw := embyDo(t, ts, http.MethodGet, path, token, "")
				var r struct {
					Items []struct{ Name string }
					Total int `json:"TotalRecordCount"`
				}
				if status != 200 || json.Unmarshal(raw, &r) != nil || r.Total != 2 || len(r.Items) != 1 || r.Items[0].Name != tc.want {
					t.Fatalf("%s: %d %s", path, status, raw)
				}
			}
		}
	}
}
