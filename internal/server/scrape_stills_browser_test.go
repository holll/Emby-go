package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestScrapeStillsBrowser exercises the embedded production page and real App
// through Chromium. Only the local MetaTube upstream is fake; no browser API,
// overlay, refresh handler, scraper, scanner, image endpoint or store is replaced.
func TestScrapeStillsBrowser(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		t.Skip("set CHROME_BIN to an existing Chromium executable for full-stack scrape stills browser regression")
	}
	if _, err := exec.LookPath(chrome); err != nil {
		t.Fatalf("CHROME_BIN: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for full-stack scrape stills browser regression")
	}
	if output, err := exec.Command(node, "-e", `if(typeof WebSocket !== 'function') process.exit(1)`).CombinedOutput(); err != nil {
		t.Skipf("Node with native WebSocket is required: %v %s", err, output)
	}
	for _, width := range []int{390, 1440} {
		t.Run(fmt.Sprintf("width%d", width), func(t *testing.T) {
			app, ts, token := contractEnv(t)
			stills := make([][]byte, 2)
			for i, width := range []int{40, 80} {
				var data bytes.Buffer
				if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, width, 24)), nil); err != nil {
					t.Fatal(err)
				}
				stills[i] = data.Bytes()
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/movies/search":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"data":[{"provider":"FANZA","id":"browser-stills","number":"ABF-018","title":"Browser stills candidate"}]}`)
				case r.URL.Path == "/v1/movies/FANZA/browser-stills":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
						"provider": "FANZA", "id": "browser-stills", "number": "ABF-018", "title": "Browser stills candidate",
						"preview_images": []string{"still-a", "still-b"},
					}})
				case strings.HasPrefix(r.URL.Path, "/v1/images/"):
					w.Header().Set("Content-Type", "image/jpeg")
					index := 0
					if remote := r.URL.Query().Get("url"); remote != "" {
						// Production previews use primary + fixed crop; confirmed
						// downloads use the original backdrop + remote URL.
						if (r.URL.Path != "/v1/images/primary/FANZA/browser-stills" && r.URL.Path != "/v1/images/backdrop/FANZA/browser-stills") || (remote != "still-a" && remote != "still-b") {
							t.Errorf("unexpected still upstream request: %s", r.URL)
							http.Error(w, "unexpected still", http.StatusBadRequest)
							return
						}
						if remote == "still-b" {
							index = 1
						}
					}
					_, _ = w.Write(stills[index])
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
			before, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			media := filepath.Dir(before.SourcePath)
			filesBefore := scrapeBrowserFiles(t, media)
			unchanged := func() error {
				after, err := app.db.Movie(1)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(filesBefore, scrapeBrowserFiles(t, media)) {
					return fmt.Errorf("preview/selection/cancel changed movie DB or media-directory files")
				}
				return nil
			}
			for _, mode := range []string{"cancel", "save"} {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				cmd := exec.CommandContext(ctx, node, "--test-reporter=tap", "scrape_stills_browser.cjs")
				cmd.Env = append(os.Environ(), "SCRAPE_BROWSER_TARGET="+ts.URL, "SCRAPE_BROWSER_MODE="+mode, fmt.Sprintf("SCRAPE_BROWSER_WIDTH=%d", width))
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				stdin, err := cmd.StdinPipe()
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					cancel()
					t.Fatal(err)
				}
				scanner := bufio.NewScanner(stdout)
				milestones, passed, skipped := 0, false, false
				var snapshotErr error
				for scanner.Scan() {
					line := scanner.Text()
					t.Logf("%s: %s", mode, line)
					if strings.Contains(line, "SCRAPE_STILLS_PRECONFIRM") {
						milestones++
						snapshotErr = unchanged()
						if snapshotErr != nil {
							cancel()
						} else if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
							snapshotErr = err
							cancel()
						}
					}
					passed = passed || strings.Contains(line, "# pass 1")
					skipped = skipped || strings.Contains(line, "# skipped 0")
				}
				stdin.Close()
				waitErr := cmd.Wait()
				cancel()
				if waitErr != nil || scanner.Err() != nil || snapshotErr != nil || milestones != 1 || !passed || !skipped {
					t.Fatalf("%s browser: wait=%v scan=%v snapshot=%v milestones=%d pass1=%v skip0=%v stderr=%s", mode, waitErr, scanner.Err(), snapshotErr, milestones, passed, skipped, stderr.String())
				}
				if mode == "cancel" {
					if err := unchanged(); err != nil {
						t.Fatal(err)
					}
				}
			}
			after, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(media, "extrafanart", "fanart1.webp"), filepath.Join(media, "extrafanart", "fanart2.webp")}
			if !reflect.DeepEqual(after.ExtraBackdrops, paths) {
				t.Fatalf("actual DB ExtraBackdrops=%v want %v", after.ExtraBackdrops, paths)
			}
			entries, err := os.ReadDir(filepath.Dir(paths[0]))
			if err != nil || len(entries) != 2 {
				t.Fatalf("extra backdrop directory: %v %v", entries, err)
			}
			for i, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				decoded, format, err := image.Decode(bytes.NewReader(data))
				wantWidth := []int{80, 40}[i]
				if err != nil || format != "webp" {
					t.Fatalf("fanart%d WebP decode: format=%q err=%v", i+1, format, err)
				}
				if decoded.Bounds().Dx() != wantWidth || decoded.Bounds().Dy() != 24 {
					t.Fatalf("fanart%d does not preserve selected [1,0] image order/dimensions", i+1)
				}
				response, clientBytes := embyRaw(t, ts, "GET", fmt.Sprintf("/Items/1/Images/Backdrop/%d", i+1), token, "")
				if response.StatusCode != http.StatusOK || !bytes.Equal(clientBytes, data) {
					t.Fatalf("Emby backdrop %d: status=%d does not match disk bytes", i+1, response.StatusCode)
				}
			}
			t.Log("real App UI -> scrape API -> ordered WebP disk -> DB ExtraBackdrops -> Emby image bytes verified; browser skips=0")
		})
	}
}

// Snapshot the entire media tree, including NFO, original poster and new paths.
func scrapeBrowserFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[path+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
