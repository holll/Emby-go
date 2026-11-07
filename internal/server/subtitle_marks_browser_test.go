package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/nfo"
)

// Explicit opt-in keeps Chromium optional for ordinary Go tests. Once opted in,
// missing runtime prerequisites and Node skips are failures, not coverage.
func TestSubtitleMarksBrowser(t *testing.T) {
	if os.Getenv("SUBTITLE_MARKS_BROWSER") != "1" {
		t.Skip("set SUBTITLE_MARKS_BROWSER=1 for real App/Chromium subtitle marks acceptance")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "-e", `if(typeof WebSocket !== 'function') process.exit(1)`).CombinedOutput(); err != nil {
		t.Fatalf("Node native WebSocket required: %v %s", err, output)
	}
	for _, width := range []int{375, 1280} {
		for _, mode := range []string{"success", "ownership"} {
			t.Run(fmt.Sprintf("width%d/%s", width, mode), func(t *testing.T) {
				app, setup, _, movie := subtitleMarksTestMovie(t, `<movie><title>Local browser movie</title><num>ABF-018</num><tag>人工保留</tag><custom>preserve-me</custom></movie>`)
				media := filepath.Dir(movie.SourcePath)
				root := filepath.Dir(media)
				path := fmt.Sprintf("/api/admin/items/%d/subtitle-marks", movie.ID)
				writeFile(t, filepath.Join(media, "ABF-018.zh.srt"), "1\n00:00:00,000 --> 00:00:01,000\n中文\n")
				for _, name := range []string{"poster.jpg", "fanart.jpg", "thumb.jpg"} {
					writeFile(t, filepath.Join(media, name), "existing image: "+name)
				}
				// No MetaTube configuration: this organizer is strictly local.
				beforeFiles := scrapeBrowserFiles(t, media)
				beforeVersion := app.db.Version("g:version")
				if _, err := app.db.CreateUser("subtitle-viewer", "password-1234", false); err != nil {
					t.Fatal(err)
				}
				resp, login := embyCall(t, setup, "POST", "/Users/AuthenticateByName", "", `{"Username":"subtitle-viewer","Pw":"password-1234"}`)
				viewerToken, _ := login["AccessToken"].(string)
				if resp.StatusCode != 200 || viewerToken == "" {
					t.Fatalf("real viewer login: %d %+v", resp.StatusCode, login)
				}
				for _, endpoint := range []string{path + "/preview", path} {
					resp, body := embyCall(t, setup, "POST", endpoint, viewerToken, `{}`)
					if resp.StatusCode != http.StatusForbidden {
						t.Fatalf("viewer %s: %d %+v", endpoint, resp.StatusCode, body)
					}
				}
				// A real SQLite abort, not an API replacement or test-only route.
				if mode == "ownership" {
					raw, err := sql.Open("sqlite", filepath.Join(root, "t.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer raw.Close()
					if _, err := raw.Exec(`CREATE TRIGGER fail_subtitle_browser_marks BEFORE UPDATE OF auto_marks ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_SUBTITLE_BROWSER_SQL'); END`); err != nil {
						t.Fatal(err)
					}
				}
				handler := app.Handler()
				previews := make(chan *autoMarksBrowserResponse, 3)
				confirms := make(chan *autoMarksBrowserResponse, 1)
				var previewCount, confirmCount atomic.Int32
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && (r.URL.Path == path || r.URL.Path == path+"/preview") {
						observed := &autoMarksBrowserResponse{ResponseWriter: w, status: http.StatusOK}
						handler.ServeHTTP(observed, r)
						if r.URL.Path == path+"/preview" {
							if previewCount.Add(1) <= 3 {
								previews <- observed
							}
						} else if confirmCount.Add(1) == 1 {
							confirms <- observed
						}
						return
					}
					handler.ServeHTTP(w, r)
				}))
				defer ts.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, node, "--test-reporter=tap", "subtitle_marks_browser.cjs")
				cmd.Env = append(os.Environ(), "SUBTITLE_BROWSER_TARGET="+ts.URL, "SUBTITLE_BROWSER_MODE="+mode, fmt.Sprintf("SUBTITLE_BROWSER_WIDTH=%d", width))
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					cancel()
					if cmd.ProcessState == nil {
						_ = cmd.Wait()
					}
				}()
				scanner := bufio.NewScanner(stdout)
				milestones, passed, noSkips := 0, false, false
				for scanner.Scan() {
					line := scanner.Text()
					t.Log(line)
					if strings.Contains(line, "SUBTITLE_PREVIEW_READY") {
						milestones++
						for i := range 3 {
							select {
							case response := <-previews:
								var preview map[string]any
								wantOwned := []any{}
								if i == 2 {
									wantOwned = []any{"中文字幕"}
								}
								if err := json.Unmarshal(response.body.Bytes(), &preview); err != nil || response.status != 200 || preview["changed"] != (i == 2) || preview["overwrite"] != (i == 2) || preview["protected"] != (i != 2) || !reflect.DeepEqual(preview["previous_owned"], []any{}) || !reflect.DeepEqual(preview["owned"], wantOwned) || preview["version"] == "" {
									t.Fatalf("actual preview status=%d body=%s err=%v", response.status, response.body.String(), err)
								}
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							}
						}
						after, err := app.db.Movie(movie.ID)
						if err != nil || !reflect.DeepEqual(movie, after) || !reflect.DeepEqual(beforeFiles, scrapeBrowserFiles(t, media)) || app.db.Version("g:version") != beforeVersion || confirmCount.Load() != 0 || previewCount.Load() != 3 {
							t.Fatalf("preview mutated NFO/media/DB or confirmed: err=%v preview=%d confirm=%d", err, previewCount.Load(), confirmCount.Load())
						}
						if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
							t.Fatal(err)
						}
					}
					if strings.Contains(line, "SUBTITLE_SAVED_READY") {
						milestones++
						select {
						case response := <-confirms:
							var result map[string]any
							wantStatus := "success"
							if mode == "ownership" {
								wantStatus = "partial_success"
							}
							if err := json.Unmarshal(response.body.Bytes(), &result); err != nil || response.status != 200 || result["status"] != wantStatus || result["saved"] != true || result["indexed"] != true || result["ownership_saved"] != (mode == "success") || strings.Contains(response.body.String(), "PRIVATE_SUBTITLE") || strings.Contains(response.body.String(), root) {
								t.Fatalf("actual confirm status=%d body=%s err=%v", response.status, response.body.String(), err)
							}
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
						if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
							t.Fatal(err)
						}
					}
					passed = passed || strings.Contains(line, "# pass 1")
					noSkips = noSkips || strings.Contains(line, "# skipped 0")
				}
				stdin.Close()
				waitErr := cmd.Wait()
				if waitErr != nil || scanner.Err() != nil || milestones != 2 || !passed || !noSkips {
					t.Fatalf("browser wait=%v scan=%v milestones=%d pass1=%v skip0=%v stderr=%s", waitErr, scanner.Err(), milestones, passed, noSkips, stderr.String())
				}
				after, err := app.db.Movie(movie.ID)
				if err != nil {
					t.Fatal(err)
				}
				meta, err := nfo.Read(movie.NFOPath)
				if err != nil || !slices.Equal(meta.Tags, []string{"人工保留", "中文字幕"}) || !slices.Equal(after.Tags, meta.Tags) || (mode == "success" && !slices.Equal(after.AutoMarks, []string{"中文字幕"})) || (mode == "ownership" && len(after.AutoMarks) != 0) || after.Title != movie.Title || after.Status != movie.Status || after.LastScrapeAt != movie.LastScrapeAt {
					t.Fatalf("actual committed NFO/index/ownership/scrape fields: meta=%+v movie=%+v err=%v", meta, after, err)
				}
				afterFiles := scrapeBrowserFiles(t, media)
				if beforeFiles[movie.NFOPath] == afterFiles[movie.NFOPath] {
					t.Fatal("confirmation did not change the real NFO")
				}
				delete(beforeFiles, movie.NFOPath)
				delete(afterFiles, movie.NFOPath)
				if !reflect.DeepEqual(beforeFiles, afterFiles) || previewCount.Load() != 3 || confirmCount.Load() != 1 {
					t.Fatalf("images/media changed or implicit retry: preview=%d confirm=%d", previewCount.Load(), confirmCount.Load())
				}
				t.Log("verified real login/App/app.js; three read-only HTTP previews; one explicit HTTP confirm; temporary NFO/SQLite, retained images, viewer 403, and Chromium skip=0")
			})
		}
	}
}
