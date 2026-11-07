package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/nfo"
)

type scrapeBodyBarrier struct {
	io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *scrapeBodyBarrier) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Reader.Read(p)
}

// Pause the real authenticated router after movieParam, during JSON decoding.
// Another HTTP confirmation commits and releases its gate before this request
// claims it, so exclusivity alone is insufficient: the DB snapshot must reload.
func TestScrapeAutoMarksReloadsAfterGate(t *testing.T) {
	app, ts, token := contractEnv(t)
	movie, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(movie.NFOPath, []byte("<movie><title>Old</title><num>ABF-018</num><tag>手工</tag><tag>中文字幕</tag></movie>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.db.SetAutoMarks(movie.ID, []string{"中文字幕"}); err != nil {
		t.Fatal(err)
	}
	var details atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/movies/test/fixture" {
			t.Errorf("unexpected upstream request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		label := "厂牌A"
		if details.Add(1) == 1 {
			label = "中文字幕"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"provider": "test", "id": "fixture", "number": "ABF-018", "title": "Saved", "label": label}})
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture", settingDownloadImages: "false"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	const confirm = `{"provider":"test","id":"fixture","overwrite":true}`
	barrier := &scrapeBodyBarrier{Reader: strings.NewReader(confirm), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer unblock()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/items/1/scrape", barrier)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", token)
	recorder := httptest.NewRecorder()
	handler := app.Handler()
	done := make(chan struct{})
	go func() { handler.ServeHTTP(recorder, req); close(done) }()
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("JSON body barrier not entered")
	}
	if details.Load() != 0 || app.nfoBusyOwner() != "" {
		t.Fatal("barrier must precede gate and upstream lookup")
	}
	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/scrape", token, confirm)
	if resp.StatusCode != http.StatusOK || bytes.Contains(body, []byte("auto_marks_error")) {
		t.Fatalf("intervening confirmation %d %s", resp.StatusCode, body)
	}
	current, err := app.db.Movie(1)
	if err != nil || len(current.AutoMarks) != 0 || app.nfoBusyOwner() != "" {
		t.Fatalf("intervening ownership=%q owner=%s err=%v", current.AutoMarks, app.nfoBusyOwner(), err)
	}
	unblock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("paused confirmation did not complete")
	}
	if recorder.Code != http.StatusOK || bytes.Contains(recorder.Body.Bytes(), []byte("auto_marks_error")) {
		t.Fatalf("paused confirmation %d %s", recorder.Code, recorder.Body.String())
	}
	current, err = app.db.Movie(1)
	if err != nil || len(current.AutoMarks) != 0 {
		t.Fatalf("paused ownership=%q err=%v", current.AutoMarks, err)
	}
	meta, err := nfo.Read(movie.NFOPath)
	// No filename or sidecar generates C. Its former ownership was relinquished
	// by the intervening commit, so using the stale [C] would wrongly delete it.
	if err != nil || !slices.Equal(meta.Tags, []string{"手工", "中文字幕", "厂牌A"}) || details.Load() != 2 {
		t.Fatalf("stale snapshot removed unowned tag: NFO=%+v lookups=%d err=%v", meta, details.Load(), err)
	}
}

// The first real upstream lookup holds the scrape gate. Neither another
// confirmation nor a confirmation overlapping a real batch may share its snapshot.
func TestScrapeAutoMarksExclusiveGate(t *testing.T) {
	for _, mode := range []string{"confirm", "batch"} {
		t.Run(mode, func(t *testing.T) {
			app, ts, token := contractEnv(t)
			movie, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(movie.NFOPath, []byte("<movie><title>Old</title><num>ABF-018</num><tag>手工</tag><tag>中文字幕</tag></movie>"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(movie.SourcePath), "ABF-018.zh.srt"), []byte("1\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := app.db.SetAutoMarks(movie.ID, []string{"中文字幕"}); err != nil {
				t.Fatal(err)
			}

			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var details, searches, images atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/v1/movies/search":
					searches.Add(1)
					_, _ = io.WriteString(w, `{"data":[{"provider":"test","id":"fixture","number":"ABF-018","title":"Saved"}]}`)
				case r.URL.Path == "/v1/movies/test/fixture":
					first := details.Add(1) == 1
					label := "厂牌A"
					if first {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						// Upstream now supplies the formerly generated text itself:
						// the first commit relinquishes ownership, retaining the tag.
						label = "中文字幕"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"provider": "test", "id": "fixture", "number": "ABF-018", "title": "Saved", "label": label}})
				case strings.HasPrefix(r.URL.Path, "/v1/images/"):
					images.Add(1)
					http.Error(w, "unexpected download", http.StatusBadGateway)
				default:
					t.Errorf("unexpected upstream request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			defer unblock()
			for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture", settingDownloadImages: "false"} {
				if err := app.db.SetSetting(key, value); err != nil {
					t.Fatal(err)
				}
			}
			const confirm = `{"provider":"test","id":"fixture","overwrite":true}`
			type outcome struct {
				status int
				body   []byte
				err    error
			}
			firstDone := make(chan outcome, 1)
			if mode == "confirm" {
				go func() {
					req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/items/1/scrape", strings.NewReader(confirm))
					if err != nil {
						firstDone <- outcome{err: err}
						return
					}
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Emby-Token", token)
					resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
					if err != nil {
						firstDone <- outcome{err: err}
						return
					}
					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					firstDone <- outcome{status: resp.StatusCode, body: body, err: err}
				}()
			} else {
				resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/scrape/run", token, `{"only_missing":false,"overwrite":true}`)
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("batch %d %s", resp.StatusCode, body)
				}
				// Registration precedes the 202 response; no new background worker is
				// started until this batch has joined and the assertions complete.
				go func() { app.workerWG.Wait(); firstDone <- outcome{status: http.StatusOK} }()
			}
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("first lookup did not enter")
			}
			before, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			beforeBytes, err := os.ReadFile(movie.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			version := app.db.Version("g:version")
			resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/scrape", token, confirm)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("overlapping confirm %d %s", resp.StatusCode, body)
			}
			after, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			afterBytes, err := os.ReadFile(movie.NFOPath)
			if err != nil || !bytes.Equal(beforeBytes, afterBytes) || !reflect.DeepEqual(before, after) || app.db.Version("g:version") != version || details.Load() != 1 || images.Load() != 0 {
				t.Fatalf("rejected request changed state: lookup=%d images=%d before=%+v after=%+v err=%v", details.Load(), images.Load(), before, after, err)
			}
			wantSearches := int32(0)
			if mode == "batch" {
				wantSearches = 1
			}
			if searches.Load() != wantSearches {
				t.Fatalf("searches=%d want=%d", searches.Load(), wantSearches)
			}
			unblock()
			select {
			case result := <-firstDone:
				if result.err != nil || result.status != http.StatusOK || bytes.Contains(result.body, []byte("auto_marks_error")) {
					t.Fatalf("first result=%+v", result)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("first scrape did not complete")
			}
			if app.nfoBusyOwner() != "" {
				t.Fatalf("gate not released: %s", app.nfoBusyOwner())
			}
			current, err := app.db.Movie(1)
			if err != nil || len(current.AutoMarks) != 0 {
				t.Fatalf("first ownership=%q err=%v", current.AutoMarks, err)
			}
			// A new independent confirmation must load that new empty ownership.
			// It preserves the existing now-unowned tag rather than adopting it.
			resp, body = embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/scrape", token, confirm)
			if resp.StatusCode != http.StatusOK || bytes.Contains(body, []byte("auto_marks_error")) {
				t.Fatalf("later confirm %d %s", resp.StatusCode, body)
			}
			current, err = app.db.Movie(1)
			if err != nil || len(current.AutoMarks) != 0 {
				t.Fatalf("later ownership=%q err=%v", current.AutoMarks, err)
			}
			meta, err := nfo.Read(movie.NFOPath)
			if err != nil || !slices.Equal(meta.Tags, []string{"手工", "中文字幕", "厂牌A"}) || details.Load() != 2 || images.Load() != 0 {
				t.Fatalf("later NFO=%+v lookup=%d images=%d err=%v", meta, details.Load(), images.Load(), err)
			}
		})
	}
}
