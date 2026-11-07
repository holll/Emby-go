package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestExtraBackdropClientEnumerationAndImages(t *testing.T) {
	app, ts, token := contractEnv(t)
	libs, err := app.db.Libraries()
	if err != nil || len(libs) != 1 {
		t.Fatalf("libraries=%v err=%v", libs, err)
	}
	root := libs[0].Path
	extraDir := filepath.Join(root, "extrafanart")
	if err := os.Mkdir(extraDir, 0755); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(root, "fanart.jpg"), filepath.Join(extraDir, "fanart2.jpg"), filepath.Join(extraDir, "fanart10.jpg")}
	for i, p := range paths {
		writeJPEGImage(t, p, []int{96, 64, 128}[i], 48)
		stamp := time.Unix(1700000000+int64(i), 0)
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(root, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	itemPath := "/Users/1/Items/" + strconv.FormatInt(id, 10)
	imageBase := "/Items/" + strconv.FormatInt(id, 10) + "/Images"

	checkSequence := func(want []string) []string {
		t.Helper()
		resp, raw := embyRaw(t, ts, "GET", itemPath, token, "")
		var dto struct {
			BackdropImageTags []string
			ImageTags         map[string]string
		}
		if resp.StatusCode != 200 || json.Unmarshal(raw, &dto) != nil || dto.BackdropImageTags == nil || len(dto.BackdropImageTags) != len(want) {
			t.Fatalf("DTO status=%d body=%s want=%v", resp.StatusCode, raw, want)
		}
		if len(want) == 0 {
			if _, exists := dto.ImageTags["Backdrop"]; exists {
				t.Fatalf("unexpected first backdrop: %v", dto.ImageTags)
			}
		} else if dto.ImageTags["Backdrop"] != dto.BackdropImageTags[0] {
			t.Fatalf("first tag differs: %+v", dto)
		}
		resp, raw = embyRaw(t, ts, "GET", imageBase, token, "")
		var info []struct {
			ImageType  string
			ImageIndex int
			Path       string
			Filename   string
			ImageTag   string
		}
		if resp.StatusCode != 200 || json.Unmarshal(raw, &info) != nil || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("image info: %d %s headers=%v", resp.StatusCode, raw, resp.Header)
		}
		count := 0
		for _, entry := range info {
			if entry.ImageType != "Backdrop" {
				continue
			}
			if count >= len(want) || entry.ImageIndex != count || entry.Path != want[count] || entry.Filename != filepath.Base(want[count]) || entry.ImageTag != dto.BackdropImageTags[count] {
				t.Fatalf("image info entry=%+v index=%d want=%v tags=%v", entry, count, want, dto.BackdropImageTags)
			}
			count++
		}
		if count != len(want) {
			t.Fatalf("info count=%d want=%d", count, len(want))
		}
		m, err := app.db.Movie(id)
		if err != nil {
			t.Fatal(err)
		}
		part := app.partItem(m, 0)
		if !reflect.DeepEqual(part["BackdropImageTags"], dto.BackdropImageTags) {
			t.Fatalf("part tags=%v movie tags=%v", part["BackdropImageTags"], dto.BackdropImageTags)
		}
		return dto.BackdropImageTags
	}
	fetch := func(route, file string) *http.Response {
		t.Helper()
		resp, raw := embyRaw(t, ts, "GET", route, "", "")
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || !bytes.Equal(raw, want) || !strings.Contains(resp.Header.Get("Cache-Control"), "public, max-age=") {
			t.Fatalf("image %s: status=%d headers=%v", route, resp.StatusCode, resp.Header)
		}
		return resp
	}
	tags := checkSequence(paths)
	for i, p := range paths {
		resp := fetch(imageBase+"/Backdrop/"+strconv.Itoa(i), p)
		if resp.Header.Get("ETag") != `"`+tags[i]+`"` {
			t.Fatalf("index %d ETag=%s tag=%s", i, resp.Header.Get("ETag"), tags[i])
		}
	}
	fetch(imageBase+"/Backdrop", paths[0])
	fetch(imageBase+"/Fanart/2", paths[2])
	fetch("/emby/items/"+strconv.FormatInt(id, 10)+"/images/backdrop/1", paths[1])
	for _, index := range []string{"-1", "abc", "999999999999999999999999999999999999"} {
		resp, _ := embyRaw(t, ts, "GET", imageBase+"/Backdrop/"+index, "", "")
		if resp.StatusCode != 400 {
			t.Fatalf("index=%s status=%d", index, resp.StatusCode)
		}
	}
	if resp, _ := embyRaw(t, ts, "GET", imageBase+"/Backdrop/3", "", ""); resp.StatusCode != 404 {
		t.Fatalf("out of range status=%d", resp.StatusCode)
	}
	if resp, _ := embyRaw(t, ts, "GET", imageBase, "", ""); resp.StatusCode != 401 {
		t.Fatalf("anonymous metadata status=%d", resp.StatusCode)
	}
	req, err := http.NewRequest("GET", ts.URL+imageBase+"/Backdrop/2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", `"`+tags[2]+`"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 304 {
		t.Fatalf("conditional extra status=%d", resp.StatusCode)
	}
	// Real JPEGs with different aspect ratios detect wrong-path thumbnail reuse.
	for i, wantHeight := range []int{16, 24, 12} {
		resp, raw := embyRaw(t, ts, "GET", fmt.Sprintf("%s/Backdrop/%d?maxWidth=32", imageBase, i), "", "")
		cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/webp" || err != nil || cfg.Width != 32 || cfg.Height != wantHeight {
			t.Fatalf("thumbnail %d status=%d config=%+v err=%v", i, resp.StatusCode, cfg, err)
		}
	}

	// Missing files retain their positions until a real scan publishes a new index.
	oldVersion := app.db.Version("g:version")
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	checkSequence(paths)
	if resp, _ := embyRaw(t, ts, "GET", imageBase+"/Backdrop/1", "", ""); resp.StatusCode != 404 {
		t.Fatalf("missing extra status=%d", resp.StatusCode)
	}
	fetch(imageBase+"/Backdrop/2", paths[2])
	scanOnce(t, ts, token)
	if app.db.Version("g:version") == oldVersion {
		t.Fatal("extra deletion did not publish a new movie cache version")
	}
	checkSequence([]string{paths[0], paths[2]})
	fetch(imageBase+"/Backdrop/1", paths[2])
	if resp, _ := embyRaw(t, ts, "GET", imageBase+"/Backdrop/2", "", ""); resp.StatusCode != 404 {
		t.Fatalf("old last index status=%d", resp.StatusCode)
	}
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if resp, _ := embyRaw(t, ts, "GET", imageBase+"/Backdrop", "", ""); resp.StatusCode != 404 {
		t.Fatalf("missing primary backdrop fell back to another file: %d", resp.StatusCode)
	}
	fetch(imageBase+"/Backdrop/1", paths[2])
	if err := os.Remove(filepath.Join(root, "poster.jpg")); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	checkSequence([]string{paths[2]}) // No poster, landscape or legacy fanart: extras-only.
	fetch(imageBase+"/Backdrop", paths[2])
	fetch(imageBase+"/Backdrop/0", paths[2])
	if err := os.Remove(paths[2]); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	checkSequence(nil)
	if resp, _ := embyRaw(t, ts, "GET", imageBase+"/Backdrop/0", "", ""); resp.StatusCode != 404 {
		t.Fatalf("empty backdrop sequence status=%d", resp.StatusCode)
	}
}

func TestMovieBackdropPathsDoesNotChangeStoredOrder(t *testing.T) {
	m := store.Movie{BackdropPath: "/main.jpg", ExtraBackdrops: []string{"/extra10.jpg", "/extra2.jpg"}}
	want := []string{"/main.jpg", "/extra10.jpg", "/extra2.jpg"}
	if got := movieBackdropPaths(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence=%v want=%v", got, want)
	}
	// The returned main+extras sequence must not append into the stored slice.
	if !reflect.DeepEqual(m.ExtraBackdrops, want[1:]) {
		t.Fatalf("stored order mutated: %v", m.ExtraBackdrops)
	}
}
