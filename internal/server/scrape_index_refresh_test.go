package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"emby-go/internal/scanner"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/scraper"
	"modernc.org/sqlite"
)

// Only local JSON fixtures are fetched: no image URLs or real media requests.
func scrapeIndexBackend(t *testing.T, a *App, cancelSecond bool) *httptest.Server {
	t.Helper()
	var searches atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		number := filepath.Base(r.URL.Path)
		if r.URL.Path == "/v1/movies/search" {
			if searches.Add(1) == 2 && cancelSecond {
				a.scrapeTaskMu.RLock()
				cancel := a.scrapeCancel
				a.scrapeTaskMu.RUnlock()
				cancel()
				http.Error(w, "cancel", 503)
				return
			}
			number = r.URL.Query().Get("q")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": number, "number": number, "provider": "test", "title": "Updated"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": number, "number": number, "provider": "test", "title": "Updated"}})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestScrapeIndexRefreshFailureIsNotSuccess(t *testing.T) {
	for _, fault := range []string{"none", "global", "movie", "commit", "canceled_index"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			a, media := detailTestApp(t, root, "<movie><title>Old</title><num>ABF-018</num></movie>")
			seedOwner(t, a)
			canceled := fault == "canceled_index"
			if canceled {
				writeFile(t, filepath.Join(media, "ABF-019.strm"), "https://media.test/b.mp4")
				writeFile(t, filepath.Join(media, "ABF-019.nfo"), "<movie><title>Old2</title><num>ABF-019</num></movie>")
			}
			lib, err := a.db.AddLibrary("L", media)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.scanLibraries(lib.ID, 0, scanner.ScanModeFull, false); err != nil {
				t.Fatal(err)
			}
			id, err := a.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
			if err != nil {
				t.Fatal(err)
			}
			old, err := a.db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			g := a.db.Version("g:version")
			raw, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			ddl := ""
			switch fault {
			case "global", "canceled_index":
				ddl = "CREATE TRIGGER fail_scrape_index BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'SCRAPE_INDEX_SQL_CANARY'); END"
			case "movie":
				ddl = "CREATE TRIGGER fail_scrape_index BEFORE UPDATE OF title ON movies BEGIN SELECT RAISE(ABORT,'SCRAPE_INDEX_SQL_CANARY'); END"
			case "commit":
				ddl = "CREATE TABLE scrape_index_parent(id INTEGER PRIMARY KEY); CREATE TABLE scrape_index_child(id INTEGER REFERENCES scrape_index_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_scrape_index AFTER UPDATE OF title ON movies BEGIN INSERT INTO scrape_index_child VALUES(999); END"
			}
			if ddl != "" {
				if _, err = raw.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.beginScrape("scrape", 1); err != nil {
				t.Fatalf("begin failed: %v", err)
			}
			backend := scrapeIndexBackend(t, a, canceled)
			err = a.executeScrape(scraper.Config{BaseURL: backend.URL, Token: "fixture-token", Concurrency: 1, Overwrite: true}, scraper.RunOptions{LibraryID: lib.ID, Overwrite: true})
			a.scrapeTaskMu.RLock()
			status := a.scrapeStatus
			a.scrapeTaskMu.RUnlock()
			a.taskMu.Lock()
			task := a.tasks[0]
			a.taskMu.Unlock()
			if e := a.db.SaveAccessToken("scrape-index-fixture-token"); e != nil {
				t.Fatal(e)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/admin/scrape/progress", nil)
			request.Header.Set("X-Emby-Token", "scrape-index-fixture-token")
			response := httptest.NewRecorder()
			a.Handler().ServeHTTP(response, request)
			var wire scrapeStatus
			if e := json.Unmarshal(response.Body.Bytes(), &wire); e != nil || response.Code != http.StatusOK || wire.Running != status.Running || wire.IndexRefreshFailed != status.IndexRefreshFailed || wire.Error != status.Error || wire.Cancelled != status.Cancelled {
				t.Fatalf("progress contract: %d %s %v", response.Code, response.Body.String(), e)
			}
			if status.Running || status.FinishedAt == "" || status.Success != 1 || status.Cancelled != canceled {
				t.Fatalf("status %+v", status)
			}
			if fault == "none" {
				if err != nil || status.IndexRefreshFailed || status.Error != "" || task.Status != "success" {
					t.Fatalf("success err=%v status=%+v task=%+v", err, status, task)
				}
			} else {
				if !errors.Is(err, errScrapeIndexRefresh) || status.Error != errScrapeIndexRefresh.Error() || !status.IndexRefreshFailed || task.Status != "failed" || !strings.Contains(task.Error, errScrapeIndexRefresh.Error()) || strings.Contains(task.Error, "CANARY") {
					t.Fatalf("failure err=%v status=%+v task=%+v", err, status, task)
				}
				if canceled && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
				current, e := a.db.Movie(id)
				if e != nil || current.Title != old.Title || a.db.Version("g:version") != g {
					t.Fatalf("failed indexing changed data/version: %+v %v", current, e)
				}
				if _, e = raw.Exec("DROP TRIGGER fail_scrape_index"); e != nil {
					t.Fatal(e)
				}
				if _, e = a.scanLibraries(lib.ID, 0, scanner.ScanModeFull, false); e != nil {
					t.Fatalf("recovery scan: %v", e)
				}
			}
			saved, e := os.ReadFile(filepath.Join(media, "ABF-018.nfo"))
			if e != nil || !strings.Contains(string(saved), "Updated") {
				t.Fatalf("saved NFO missing: %s %v", saved, e)
			}
			current, e := a.db.Movie(id)
			if e != nil || current.Title != "ABF-018 Updated" {
				t.Fatalf("indexed data: %+v %v", current, e)
			}
			if err := a.beginScrape("scrape", 0); err != nil {
				t.Fatalf("running gate leaked: %v", err)
			}
			a.endScrape(nil)
			if a.scrapeStatus.IndexRefreshFailed || a.scrapeStatus.Error != "" {
				t.Fatal("failure carried to next run")
			}
		})
	}
}

var scrapeIndexBarrierSerial atomic.Uint64

func TestScrapeStaysRunningUntilIndexRefreshReturns(t *testing.T) {
	name := fmt.Sprintf("scrape_index_barrier_%d", scrapeIndexBarrierSerial.Add(1))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	if err := sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return int64(1), nil
		case <-time.After(10 * time.Second):
			return nil, errors.New("barrier timeout")
		}
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a, media := detailTestApp(t, root, "<movie><title>Old</title><num>ABF-018</num></movie>")
	lib, err := a.db.AddLibrary("L", media)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.scanLibraries(lib.ID, 0, scanner.ScanModeFull, false); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec("CREATE TRIGGER scrape_index_wait BEFORE UPDATE OF title ON movies BEGIN SELECT " + name + "(); END"); err != nil {
		t.Fatal(err)
	}
	backend := scrapeIndexBackend(t, a, false)
	if err := a.beginScrape("scrape", 1); err != nil {
		t.Fatalf("begin failed: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- a.executeScrape(scraper.Config{BaseURL: backend.URL, Token: "fixture-token", Concurrency: 1, Overwrite: true}, scraper.RunOptions{LibraryID: lib.ID, Overwrite: true})
	}()
	joined := false
	defer func() {
		unblock()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("worker not joined")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("index barrier not reached")
	}
	a.scrapeTaskMu.RLock()
	status := a.scrapeStatus
	a.scrapeTaskMu.RUnlock()
	a.taskMu.Lock()
	task := a.tasks[0]
	a.taskMu.Unlock()
	if !status.Running || status.FinishedAt != "" || status.Success != 1 || task.Status != "running" || task.EndedAt != "" {
		t.Fatalf("premature finish: %+v %+v", status, task)
	}
	if err := a.beginScrape("scrape", 0); err == nil {
		t.Error("new scrape accepted during indexing")
	}
	unblock()
	// Join before checking terminal state.
	select {
	case err = <-done:
		joined = true
	case <-time.After(10 * time.Second):
		t.Fatal("index did not finish")
	}
	if err != nil {
		t.Fatal(err)
	}
	a.scrapeTaskMu.RLock()
	status = a.scrapeStatus
	a.scrapeTaskMu.RUnlock()
	if status.Running || status.FinishedAt == "" {
		t.Fatalf("not finalized: %+v", status)
	}
}
