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

	"emby-go/internal/scraper"
)

// Explicit zero/false options must survive the HTTP flow, not become omitted defaults.
func TestScrapeZeroOptionsHTTPRoundTrip(t *testing.T) {
	app, ts, token := contractEnv(t)
	var fixture bytes.Buffer
	if err := jpeg.Encode(&fixture, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lookups []url.Values
	var images []url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/v1/movies/FANZA/opaque+id":
			lookups = append(lookups, r.URL.Query())
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"opaque+id","number":"NEW-002","title":"confirmed title"}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/images/"):
			q := r.URL.Query()
			q.Set("kind", strings.Split(r.URL.Path, "/")[3])
			images = append(images, q)
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(fixture.Bytes())
		default:
			t.Errorf("unexpected upstream request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	before, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfoBefore, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(before.SourcePath)
	filesBefore, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := embyRaw(t, ts, "GET", "/api/admin/items/1/scrape/preview?metatube_id="+url.QueryEscape("FANZA:opaque%2Bid:0:false"), token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview: %d %s", resp.StatusCode, raw)
	}
	var preview scraper.PreviewResult
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Candidates) != 1 {
		t.Fatalf("candidates = %+v", preview.Candidates)
	}
	candidate := preview.Candidates[0]
	if candidate.ID != "opaque+id" || candidate.Position == nil || *candidate.Position != 0 || candidate.Update == nil || *candidate.Update {
		t.Fatalf("zero/false options or identity changed: %+v", candidate)
	}
	body, err := json.Marshal(map[string]any{
		"provider": candidate.Provider, "id": candidate.ID,
		"position": candidate.Position, "update": candidate.Update, "overwrite": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, raw = embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token, string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inspect: %d %s", resp.StatusCode, raw)
	}
	var inspected scrapeInspect
	if err := json.Unmarshal(raw, &inspected); err != nil {
		t.Fatal(err)
	}
	poster, err := url.Parse(inspected.Poster)
	if err != nil {
		t.Fatal(err)
	}
	if q := poster.Query(); !q.Has("pos") || q.Get("pos") != "0" || q.Get("id") != "opaque+id" {
		t.Fatalf("poster URL lost explicit zero or identity: %s", poster)
	}
	resp, raw = embyRaw(t, ts, "GET", inspected.Poster, "", "")
	if resp.StatusCode != http.StatusOK || !bytes.Equal(raw, fixture.Bytes()) {
		t.Fatalf("poster proxy: %d, %d bytes", resp.StatusCode, len(raw))
	}
	// Preview, inspect and anonymous image reads are not a confirmation.
	after, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfoAfter, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	filesAfter, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !bytes.Equal(nfoBefore, nfoAfter) || !reflect.DeepEqual(filesBefore, filesAfter) {
		t.Fatal("read-only flow changed movie, NFO or directory entries")
	}
	resp, raw = embyRaw(t, ts, "POST", "/api/admin/items/1/scrape", token, string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d %s", resp.StatusCode, raw)
	}
	var result struct {
		Images       int      `json:"images"`
		Warnings     []string `json:"warnings"`
		RefreshError string   `json:"refresh_error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Images != 3 || len(result.Warnings) != 0 || result.RefreshError != "" {
		t.Fatalf("confirm did not save all images and index: %s", raw)
	}
	after, err = app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "NEW-002 confirmed title" || after.ProviderID != "FANZA:opaque%2Bid" {
		t.Fatalf("confirmed movie identity/title = %q / %q", after.ProviderID, after.Title)
	}
	for _, name := range []string{"poster.webp", "fanart.webp", "landscape.webp"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != 4 || config.Height != 4 {
			t.Fatalf("%s is not the expected decodable image: %+v %v", name, config, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lookups) != 3 || len(images) != 4 {
		t.Fatalf("lookup/image counts = %d/%d, want 3/4", len(lookups), len(images))
	}
	for _, q := range lookups {
		if q.Get("lazy") != "true" {
			t.Errorf("Update=false must stay cache-first: %v", q)
		}
	}
	for _, q := range images {
		if q.Get("kind") == "primary" {
			if !q.Has("pos") || q.Get("pos") != "0" {
				t.Errorf("primary lost explicit zero: %v", q)
			}
		} else if q.Has("pos") {
			t.Errorf("wide image received primary-only crop: %v", q)
		}
	}
}
