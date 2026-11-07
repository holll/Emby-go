package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
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
	"emby-go/internal/scanner"
)

// Observe the real HTTP response without replacing the App or its response.
// These scrape JSON handlers do not stream or hijack their connection.
type autoMarksBrowserResponse struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *autoMarksBrowserResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *autoMarksBrowserResponse) Write(p []byte) (int, error) {
	w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

// TestScrapeAutoMarksBrowser covers the real production partial-success page.
// Only MetaTube and the SQLite failure triggers are fixtures, not browser APIs.
func TestScrapeAutoMarksBrowser(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		t.Skip("set CHROME_BIN to an existing Chromium executable for full-stack auto marks browser regression")
	}
	if _, err := exec.LookPath(chrome); err != nil {
		t.Fatalf("CHROME_BIN: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for full-stack auto marks browser regression")
	}
	if output, err := exec.Command(node, "-e", `if(typeof WebSocket !== 'function') process.exit(1)`).CombinedOutput(); err != nil {
		t.Skipf("Node with native WebSocket is required: %v %s", err, output)
	}
	const title = `Browser saved <img src=x onerror="window.__autoMarksInjected=1"> &`
	for _, width := range []int{390, 1440} {
		for _, mode := range []string{"ownership", "all"} {
			t.Run(fmt.Sprintf("width%d/%s", width, mode), func(t *testing.T) {
				root := t.TempDir()
				app, media := detailTestApp(t, root, `<movie><title>Old</title><num>ABF-018</num><tag>手工</tag></movie>`)
				// Initialize the real authentication system before a fresh browser
				// logs in itself. The initialization token is not given to Node.
				setup, _ := detailServer(t, app)
				setup.Close()
				lib, err := app.db.AddLibrary("L", media)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := app.scanLibraries(lib.ID, 0, scanner.ScanModeFull, false); err != nil {
					t.Fatal(err)
				}
				before, err := app.db.Movie(1)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(media, "ABF-018.zh.srt"), "1\n")
				raw, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec("CREATE TRIGGER fail_auto_marks BEFORE UPDATE OF auto_marks ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_AUTO_MARKS_SQL_CANARY'); END"); err != nil {
					t.Fatal(err)
				}
				if mode == "all" {
					if _, err := raw.Exec("CREATE TRIGGER fail_auto_marks_index BEFORE UPDATE OF title ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_AUTO_MARKS_INDEX_CANARY'); END"); err != nil {
						t.Fatal(err)
					}
				}
				var jpegData bytes.Buffer
				if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 40, 60)), nil); err != nil {
					t.Fatal(err)
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					movie := map[string]any{"provider": "FANZA", "id": "browser-auto-marks", "number": "ABF-018", "title": title, "label": "厂牌A"}
					switch {
					case r.URL.Path == "/v1/movies/search":
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{movie}})
					case r.URL.Path == "/v1/movies/FANZA/browser-auto-marks":
						_ = json.NewEncoder(w).Encode(map[string]any{"data": movie})
					case strings.HasPrefix(r.URL.Path, "/v1/images/"):
						if mode == "all" {
							http.Error(w, "fixture image unavailable", http.StatusServiceUnavailable)
							return
						}
						w.Header().Set("Content-Type", "image/jpeg")
						_, _ = w.Write(jpegData.Bytes())
					default:
						t.Errorf("unexpected MetaTube request: %s", r.URL)
						http.NotFound(w, r)
					}
				}))
				defer upstream.Close()
				for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "browser-fixture-only"} {
					if err := app.db.SetSetting(key, value); err != nil {
						t.Fatal(err)
					}
				}
				handler := app.Handler()
				responses := make(chan *autoMarksBrowserResponse, 1)
				var confirms atomic.Int32
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/api/admin/items/1/scrape" {
						count := confirms.Add(1)
						observed := &autoMarksBrowserResponse{ResponseWriter: w, status: http.StatusOK}
						handler.ServeHTTP(observed, r)
						if count == 1 {
							responses <- observed
						}
						return
					}
					handler.ServeHTTP(w, r)
				}))
				defer ts.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, node, "--test-reporter=tap", "scrape_auto_marks_browser.cjs")
				cmd.Env = append(os.Environ(), "SCRAPE_BROWSER_TARGET="+ts.URL, "SCRAPE_BROWSER_MODE="+mode, fmt.Sprintf("SCRAPE_BROWSER_WIDTH=%d", width))
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
				var savedFiles map[string]string
				var savedMovie = before
				scanner := bufio.NewScanner(stdout)
				milestones, passed, noSkips := 0, false, false
				var snapshotErr error
				for scanner.Scan() {
					line := scanner.Text()
					t.Log(line)
					if strings.Contains(line, "SCRAPE_AUTO_MARKS_SAVED") {
						milestones++
						select {
						case response := <-responses:
							var result struct {
								Status         string   `json:"status"`
								AutoMarksError string   `json:"auto_marks_error"`
								RefreshError   string   `json:"refresh_error"`
								Warnings       []string `json:"warnings"`
							}
							if err := json.Unmarshal(response.body.Bytes(), &result); err != nil || response.status != 200 || result.Status != "success" || !strings.Contains(result.AutoMarksError, "人工核对") || (result.RefreshError != "") != (mode == "all") || (len(result.Warnings) > 0) != (mode == "all") || strings.Contains(response.body.String(), "PRIVATE_AUTO_MARKS") || strings.Contains(response.body.String(), root) {
								snapshotErr = fmt.Errorf("unexpected real confirm response status=%d body=%s err=%v", response.status, response.body.String(), err)
							}
						case <-ctx.Done():
							snapshotErr = ctx.Err()
						}
						if snapshotErr == nil {
							savedMovie, snapshotErr = app.db.Movie(before.ID)
						}
						if snapshotErr == nil {
							meta, err := nfo.Read(before.NFOPath)
							if err != nil || !strings.Contains(meta.Title, title) || !slices.Equal(meta.Tags, []string{"手工", "厂牌A", "中文字幕"}) || len(savedMovie.AutoMarks) != 0 || !strings.Contains(savedMovie.LastScrapeError, "人工核对") || confirms.Load() != 1 {
								snapshotErr = fmt.Errorf("actual NFO/DB/count inconsistent: meta=%+v movie=%+v count=%d err=%v", meta, savedMovie, confirms.Load(), err)
							}
							if (mode == "all" && savedMovie.Title != before.Title) || (mode == "ownership" && !strings.Contains(savedMovie.Title, title)) {
								snapshotErr = fmt.Errorf("actual index result title=%q mode=%s", savedMovie.Title, mode)
							}
						}
						if snapshotErr == nil {
							savedFiles = scrapeBrowserFiles(t, media)
							_, snapshotErr = fmt.Fprintln(stdin, "continue")
						}
						if snapshotErr != nil {
							cancel()
						}
					}
					passed = passed || strings.Contains(line, "# pass 1")
					noSkips = noSkips || strings.Contains(line, "# skipped 0")
				}
				stdin.Close()
				waitErr := cmd.Wait()
				if waitErr != nil || scanner.Err() != nil || snapshotErr != nil || milestones != 1 || !passed || !noSkips {
					t.Fatalf("browser wait=%v scan=%v snapshot=%v milestones=%d pass1=%v skip0=%v stderr=%s", waitErr, scanner.Err(), snapshotErr, milestones, passed, noSkips, stderr.String())
				}
				after, err := app.db.Movie(before.ID)
				if err != nil || !reflect.DeepEqual(savedMovie, after) || !reflect.DeepEqual(savedFiles, scrapeBrowserFiles(t, media)) || confirms.Load() != 1 {
					t.Fatalf("Done rewrote committed state: confirms=%d DBerr=%v", confirms.Load(), err)
				}
				t.Log("real browser partial-success panel -> real HTTP 200 -> committed NFO/unowned DB/manual diagnosis -> keyboard Done without retries verified")
			})
		}
	}
}
