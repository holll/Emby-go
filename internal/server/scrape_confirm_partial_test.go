package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
)

// Exercise the real HTTP -> scraper -> NFO -> index path; only the remote
// MetaTube response and a failing SQLite write are fixtures.
func TestScrapeConfirmReportsPartialSuccess(t *testing.T) {
	for _, mode := range []string{"success", "images", "index", "both"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			app, media := detailTestApp(t, root, "<movie><title>Old</title><num>ABF-018</num></movie>")
			ts, token := detailServer(t, app)
			lib, err := app.db.AddLibrary("L", media)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = app.scanLibraries(lib.ID, 0, scanner.ScanModeFull, false); err != nil {
				t.Fatal(err)
			}
			id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
			if err != nil {
				t.Fatal(err)
			}
			old, err := app.db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			version := app.db.Version("g:version")
			raw, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			indexFault := mode == "index" || mode == "both"
			imageFault := mode == "images" || mode == "both"
			if indexFault {
				if _, err = raw.Exec("CREATE TRIGGER fail_confirm_refresh BEFORE UPDATE OF title ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_CONFIRM_SQL_CANARY'); END"); err != nil {
					t.Fatal(err)
				}
			}
			var jpegBytes bytes.Buffer
			if err := jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 4, 6)), nil); err != nil {
				t.Fatal(err)
			}
			var lookups, images atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/movies/test/fixture" {
					lookups.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":{"provider":"test","id":"fixture","number":"ABF-018","title":"Updated"}}`))
					return
				}
				if strings.HasPrefix(r.URL.Path, "/v1/images/") {
					images.Add(1)
					if imageFault {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "image/jpeg")
					_, _ = w.Write(jpegBytes.Bytes())
					return
				}
				t.Errorf("unexpected upstream request %s", r.URL)
				http.NotFound(w, r)
			}))
			defer upstream.Close()
			for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture-token"} {
				if err := app.db.SetSetting(key, value); err != nil {
					t.Fatal(err)
				}
			}
			resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/scrape", token, `{"provider":"test","id":"fixture","overwrite":true}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("confirm %d %s", resp.StatusCode, body)
			}
			var result struct {
				Status       string   `json:"status"`
				Title        string   `json:"title"`
				Images       int      `json:"images"`
				Warnings     []string `json:"warnings"`
				RefreshError string   `json:"refresh_error"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "success" || !strings.Contains(result.Title, "Updated") {
				t.Fatalf("result=%s", body)
			}
			if (result.RefreshError != "") != indexFault || (len(result.Warnings) > 0) != imageFault {
				t.Fatalf("lost partial result: %s", body)
			}
			if indexFault && !strings.Contains(result.RefreshError, "重新扫描") {
				t.Fatalf("missing recovery guidance: %s", body)
			}
			if !indexFault && bytes.Contains(body, []byte(`"refresh_error"`)) {
				t.Fatalf("optional field present on success: %s", body)
			}
			if bytes.Contains(body, []byte("PRIVATE_CONFIRM_SQL_CANARY")) || bytes.Contains(body, []byte(root)) {
				t.Fatalf("private index details exposed: %s", body)
			}
			if imageFault {
				if result.Images != 0 || len(result.Warnings) != 3 {
					t.Fatalf("image warnings lost: %s", body)
				}
			} else if result.Images != 3 {
				t.Fatalf("image success count: %s", body)
			}
			if lookups.Load() != 1 || images.Load() != 3 {
				t.Fatalf("unexpected retries: lookups=%d images=%d", lookups.Load(), images.Load())
			}
			saved, err := os.ReadFile(old.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := nfo.Read(old.NFOPath)
			if err != nil || !strings.Contains(meta.Title, "Updated") || meta.ProviderID() != "test:fixture" {
				t.Fatalf("NFO not committed: %+v %v", meta, err)
			}
			current, err := app.db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			if indexFault {
				if current.Title != old.Title || app.db.Version("g:version") != version {
					t.Fatalf("failed refresh changed index: %+v", current)
				}
				if _, err := raw.Exec("DROP TRIGGER fail_confirm_refresh"); err != nil {
					t.Fatal(err)
				}
				if _, err := scanner.RescanOne(app.db, lib, old.SourcePath); err != nil {
					t.Fatal(err)
				}
				recovered, err := app.db.Movie(id)
				if err != nil || !strings.Contains(recovered.Title, "Updated") {
					t.Fatalf("recovery failed: %+v %v", recovered, err)
				}
				after, err := os.ReadFile(old.NFOPath)
				if err != nil || !bytes.Equal(saved, after) {
					t.Fatal("recovery rewrote committed NFO", err)
				}
				if lookups.Load() != 1 || images.Load() != 3 {
					t.Fatal("index recovery repeated remote writes")
				}
			} else if !strings.Contains(current.Title, "Updated") {
				t.Fatalf("index not refreshed: %+v", current)
			}
			if !app.claimNFO("partial-test") {
				t.Fatal("confirm leaked NFO write gate")
			}
			app.releaseNFO("partial-test")
		})
	}
}

func TestScrapeConfirmLookupFailureDoesNotCommit(t *testing.T) {
	app, ts, token := contractEnv(t)
	movie, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/scrape", token, `{"provider":"test","id":"fixture","overwrite":true}`)
	if resp.StatusCode != http.StatusBadGateway || bytes.Contains(body, []byte(`"refresh_error"`)) || bytes.Contains(body, []byte(`"status":"success"`)) {
		t.Fatalf("pre-commit failure misreported: %d %s", resp.StatusCode, body)
	}
	after, err := os.ReadFile(movie.NFOPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("lookup failure modified NFO", err)
	}
	current, err := app.db.Movie(movie.ID)
	if err != nil || current.Title != movie.Title || lookups.Load() != 1 {
		t.Fatalf("lookup failure changed index or retried: %+v %v, calls=%d", current, err, lookups.Load())
	}
}
