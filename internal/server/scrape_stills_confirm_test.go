package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestScrapeSelectedStillsConfirm(t *testing.T) {
	app, ts, token := contractEnv(t)
	jpegBytes := func(width int) []byte {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, 24)), nil); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	a, b := jpegBytes(40), jpegBytes(80)
	var mu sync.Mutex
	list := []string{" a ", "b", "a", ""}
	failed := ""
	lookupFailed := false
	lookups, downloads := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/v1/movies/FANZA/selected" {
			lookups++
			if lookups > 1 && r.URL.Query().Get("lazy") != "false" {
				t.Errorf("selected confirm did not forward update=true: %s", r.URL.RawQuery)
			}
			if lookupFailed {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"provider": "FANZA", "id": "selected", "number": "NEW-003", "title": "Selected", "preview_images": list}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/images/") {
			downloads++
			q := r.URL.Query()
			if remote := q.Get("url"); remote != "" {
				if r.URL.Path != "/v1/images/backdrop/FANZA/selected" || q.Has("pos") || q.Has("badge") {
					t.Errorf("unexpected still request: %s", r.URL)
				}
				if remote == failed {
					_, _ = w.Write([]byte("invalid image"))
					return
				}
				if remote == "b" {
					_, _ = w.Write(b)
					return
				}
				if remote != "a" {
					t.Errorf("untrusted remote URL: %q", remote)
				}
			}
			_, _ = w.Write(a)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	movie, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(movie.SourcePath), "extrafanart")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	tail := filepath.Join(dir, "fanart3.jpg")
	if err := os.WriteFile(tail, a, 0644); err != nil {
		t.Fatal(err)
	}
	resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token, `{"provider":"FANZA","id":"selected"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("inspect %d %s", resp.StatusCode, raw)
	}
	var inspected scrapeInspect
	if err := json.Unmarshal(raw, &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected.Stills) != 2 {
		t.Fatalf("stills=%+v", inspected.Stills)
	}
	parsed, err := url.Parse(inspected.Stills[0].Preview)
	if err != nil {
		t.Fatal(err)
	}
	version := parsed.Query().Get("version")
	confirm := func(selection any, overwrite bool) (*http.Response, []byte) {
		body, err := json.Marshal(map[string]any{"provider": "FANZA", "id": "selected", "overwrite": overwrite, "update": true, "stills": selection})
		if err != nil {
			t.Fatal(err)
		}
		return embyRaw(t, ts, "POST", "/api/admin/items/1/scrape", token, string(body))
	}
	selected := map[string]any{"version": version, "indices": []int{1, 0}}
	// Invalid selections never perform lookups or writes.
	mu.Lock()
	beforeLookups, beforeDownloads := lookups, downloads
	mu.Unlock()
	for _, selection := range []any{
		map[string]any{"version": "", "indices": []int{0}},
		map[string]any{"version": strings.Repeat("G", 64), "indices": []int{0}},
		map[string]any{"version": version, "indices": []int{}},
		map[string]any{"version": version, "indices": []int{-1}},
		map[string]any{"version": version, "indices": []int{24}},
		map[string]any{"version": version, "indices": []int{0, 0}},
		map[string]any{"version": version, "indices": []float64{0.5}},
		map[string]any{"version": version, "indices": []any{nil}},
		map[string]any{"version": version, "indices": []any{1, nil}},
		map[string]any{"version": version, "indices": []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24}},
	} {
		resp, raw = confirm(selection, true)
		if resp.StatusCode != 400 {
			t.Fatalf("invalid selection %+v: %d %s", selection, resp.StatusCode, raw)
		}
	}
	mu.Lock()
	invalidLookups, invalidDownloads := lookups, downloads
	mu.Unlock()
	if invalidLookups != beforeLookups || invalidDownloads != beforeDownloads {
		t.Fatal("invalid selection reached upstream")
	}
	if !app.claimNFO("scan") {
		t.Fatal("could not occupy NFO gate")
	}
	resp, raw = confirm(selected, true)
	app.releaseNFO("scan")
	mu.Lock()
	busyLookups := lookups
	mu.Unlock()
	if resp.StatusCode != 409 || busyLookups != beforeLookups {
		t.Fatalf("busy NFO gate reached lookup: %d %s", resp.StatusCode, raw)
	}
	// Even while the old inspect snapshot is cached, a reordered fresh list is rejected.
	nfoBefore, err := os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	list = []string{"b", "a"}
	mu.Unlock()
	resp, raw = confirm(selected, true)
	if resp.StatusCode != 409 {
		t.Fatalf("stale version: %d %s", resp.StatusCode, raw)
	}
	after, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfoAfter, err := os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	staleDownloads := downloads
	mu.Unlock()
	if !reflect.DeepEqual(movie, after) || !bytes.Equal(nfoBefore, nfoAfter) || len(entries) != 1 || staleDownloads != beforeDownloads {
		t.Fatal("stale version changed files/movie or downloaded images")
	}
	mu.Lock()
	lookupFailed = true
	mu.Unlock()
	resp, raw = confirm(selected, true)
	if resp.StatusCode != 502 {
		t.Fatalf("lookup failure: %d %s", resp.StatusCode, raw)
	}
	after, err = app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfoAfter, err = os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	failedDownloads := downloads
	lookupFailed = false
	list = []string{"a", "b"}
	mu.Unlock()
	if !reflect.DeepEqual(movie, after) || !bytes.Equal(nfoBefore, nfoAfter) || failedDownloads != beforeDownloads {
		t.Fatal("lookup failure changed movie/NFO or downloaded images")
	}
	resp, raw = confirm(map[string]any{"version": version, "indices": []int{2}}, true)
	if resp.StatusCode != 400 {
		t.Fatalf("out of list: %d %s", resp.StatusCode, raw)
	}
	after, err = app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfoAfter, err = os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	boundsDownloads := downloads
	mu.Unlock()
	if !reflect.DeepEqual(movie, after) || !bytes.Equal(nfoBefore, nfoAfter) || len(entries) != 1 || boundsDownloads != beforeDownloads {
		t.Fatal("out-of-list selection changed movie/files or downloaded images")
	}
	resp, raw = confirm(selected, true)
	if resp.StatusCode != 200 {
		t.Fatalf("confirm %d %s", resp.StatusCode, raw)
	}
	var result struct {
		Images       int      `json:"images"`
		Warnings     []string `json:"warnings"`
		RefreshError string   `json:"refresh_error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Images != 5 || len(result.Warnings) != 0 || result.RefreshError != "" {
		t.Fatalf("confirm result %s", raw)
	}
	after, err = app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(dir, "fanart1.jpg"), filepath.Join(dir, "fanart2.jpg"), tail}
	if !reflect.DeepEqual(after.ExtraBackdrops, paths) {
		t.Fatalf("index=%v", after.ExtraBackdrops)
	}
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("not JPEG %s: %v", path, err)
		}
		width := 40
		if i == 0 {
			width = 80
		}
		if cfg.Width != width {
			t.Fatalf("selection order: %s width=%d", path, cfg.Width)
		}
	}
	tailAfter, err := os.ReadFile(tail)
	if err != nil || !bytes.Equal(tailAfter, a) {
		t.Fatal("unselected tail changed")
	}
	resp, raw = embyRaw(t, ts, "GET", "/Items/1/Images/Backdrop/1", "", "")
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if resp.StatusCode != 200 || err != nil || cfg.Width != 80 {
		t.Fatalf("client image %d %+v %v", resp.StatusCode, cfg, err)
	}
	// One corrupt download preserves the old slot while another succeeds and NFO refreshes.
	oldSecond, err := os.ReadFile(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	failed = "b"
	mu.Unlock()
	resp, raw = confirm(map[string]any{"version": version, "indices": []int{0, 1}}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("partial %d %s", resp.StatusCode, raw)
	}
	result.Warnings = nil
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Images != 4 || len(result.Warnings) != 1 || result.RefreshError != "" {
		t.Fatalf("partial result %s", raw)
	}
	second, err := os.ReadFile(paths[1])
	if err != nil || !bytes.Equal(second, oldSecond) {
		t.Fatal("failed JPEG replaced old target")
	}
	mu.Lock()
	priorDownloads := downloads
	mu.Unlock()
	resp, raw = confirm(selected, false)
	if resp.StatusCode != 200 {
		t.Fatalf("skip existing %d %s", resp.StatusCode, raw)
	}
	result.Warnings = nil
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	skipDownloads := downloads
	mu.Unlock()
	if result.Images != 0 || len(result.Warnings) != 0 || skipDownloads != priorDownloads {
		t.Fatalf("skip existing result %s", raw)
	}
	if err := app.db.SetSetting(settingDownloadImages, "false"); err != nil {
		t.Fatal(err)
	}
	resp, raw = confirm(selected, true)
	if resp.StatusCode != 200 {
		t.Fatalf("images disabled: %d %s", resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	disabledDownloads := downloads
	mu.Unlock()
	if result.Images != 0 || disabledDownloads != priorDownloads {
		t.Fatalf("images disabled downloaded: %s", raw)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "tmp") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}
