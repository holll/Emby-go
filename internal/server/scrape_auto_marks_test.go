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
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/scraper"
)

// Only the remote MetaTube server and SQLite failure triggers are fixtures.
func TestScrapeAutoMarksPartialSuccess(t *testing.T) {
	for _, mode := range []string{"success", "ownership", "index", "all"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			app, media := detailTestApp(t, root, "<movie><title>Old</title><num>ABF-018</num><tag>手工</tag></movie>")
			ts, token := detailServer(t, app)
			lib, err := app.db.AddLibrary("L", media)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.scanLibraries(lib.ID, 0, scanner.ScanModeFull, false); err != nil {
				t.Fatal(err)
			}
			old, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(media, "ABF-018.zh.srt"), []byte("1\n"), 0644); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			ownershipFault := mode == "ownership" || mode == "all"
			indexFault := mode == "index" || mode == "all"
			imageFault := mode == "all"
			if ownershipFault {
				if _, err := raw.Exec("CREATE TRIGGER fail_auto_marks BEFORE UPDATE OF auto_marks ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_AUTO_MARKS_SQL_CANARY'); END"); err != nil {
					t.Fatal(err)
				}
			}
			if indexFault {
				if _, err := raw.Exec("CREATE TRIGGER fail_auto_marks_index BEFORE UPDATE OF title ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_AUTO_MARKS_INDEX_CANARY'); END"); err != nil {
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
					_, _ = w.Write([]byte(`{"data":{"provider":"test","id":"fixture","number":"ABF-018","title":"Updated","label":"厂牌A"}}`))
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
				t.Errorf("unexpected upstream request: %s", r.URL)
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
				Status         string   `json:"status"`
				AutoMarksError string   `json:"auto_marks_error"`
				RefreshError   string   `json:"refresh_error"`
				Warnings       []string `json:"warnings"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "success" || (result.AutoMarksError != "") != ownershipFault || (result.RefreshError != "") != indexFault || (len(result.Warnings) > 0) != imageFault {
				t.Fatalf("partial fields=%s", body)
			}
			if !ownershipFault && bytes.Contains(body, []byte(`"auto_marks_error"`)) {
				t.Fatalf("optional field present on success: %s", body)
			}
			if ownershipFault && (!strings.Contains(result.AutoMarksError, "人工核对") || !strings.Contains(result.AutoMarksError, "重扫不能恢复") || !strings.Contains(result.AutoMarksError, "不要直接重新刮削")) {
				t.Fatalf("unsafe recovery guidance: %s", body)
			}
			if bytes.Contains(body, []byte("PRIVATE_AUTO_MARKS")) || bytes.Contains(body, []byte(root)) {
				t.Fatalf("private SQL/path exposed: %s", body)
			}
			if lookups.Load() != 1 || images.Load() != 3 {
				t.Fatalf("unexpected retries: lookup=%d images=%d", lookups.Load(), images.Load())
			}
			saved, err := os.ReadFile(old.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := nfo.Read(old.NFOPath)
			if err != nil || !strings.Contains(meta.Title, "Updated") || !slices.Equal(meta.Tags, []string{"手工", "厂牌A", "中文字幕"}) {
				t.Fatalf("NFO not committed: %+v %v", meta, err)
			}
			current, err := app.db.Movie(old.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantOwned := []string{"中文字幕"}
			if ownershipFault {
				wantOwned = nil
			}
			if !slices.Equal(current.AutoMarks, wantOwned) {
				t.Fatalf("ownership=%q want=%q", current.AutoMarks, wantOwned)
			}
			if ownershipFault && !strings.Contains(current.LastScrapeError, "人工核对") {
				t.Fatalf("missing durable diagnosis: %+v", current)
			}
			if indexFault && current.Title != old.Title {
				t.Fatalf("failed refresh changed title: %+v", current)
			}
			if !indexFault && !strings.Contains(current.Title, "Updated") {
				t.Fatalf("missing index refresh: %+v", current)
			}
			if ownershipFault {
				if _, err := raw.Exec("DROP TRIGGER fail_auto_marks"); err != nil {
					t.Fatal(err)
				}
			}
			if indexFault {
				if _, err := raw.Exec("DROP TRIGGER fail_auto_marks_index"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := scanner.RescanOne(app.db, lib, old.SourcePath); err != nil {
				t.Fatal(err)
			}
			rescanned, err := app.db.Movie(old.ID)
			if err != nil || !slices.Equal(rescanned.AutoMarks, wantOwned) || !strings.Contains(rescanned.Title, "Updated") {
				t.Fatalf("rescan invented ownership: %+v %v", rescanned, err)
			}
			after, err := os.ReadFile(old.NFOPath)
			if err != nil || !bytes.Equal(saved, after) || lookups.Load() != 1 || images.Load() != 3 {
				t.Fatal("rescan rewrote NFO or retried upstream", err)
			}
			if !app.claimNFO("auto-marks-test") {
				t.Fatal("NFO gate leaked")
			}
			app.releaseNFO("auto-marks-test")
		})
	}
}

func TestDisplaySavedAutoMarksDiagnostic(t *testing.T) {
	text := displayItem(scraper.ItemResult{Status: scraper.StatusSuccess, Title: "Saved", Reason: "NFO 已保存，需人工核对"})
	if !strings.HasPrefix(text, "已保存待核对 ") || strings.HasPrefix(text, "失败") {
		t.Fatalf("partial success mislabeled: %s", text)
	}
}
