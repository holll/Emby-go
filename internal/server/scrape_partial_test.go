package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"emby-go/internal/scraper"
)

func TestCanceledBatchRefreshesAlreadyWrittenNFO(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), "<movie><title>Old</title><num>ABF-018</num></movie>")
	writeFile(t, filepath.Join(media, "ABF-019.strm"), "https://media.test/b.mp4")
	writeFile(t, filepath.Join(media, "ABF-019.nfo"), "<movie><title>Old2</title><num>ABF-019</num></movie>")
	lib, err := app.db.AddLibrary("L", media)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.scanLibraries(lib.ID); err != nil {
		t.Fatal(err)
	}
	if !app.beginScrape("scrape", 2) {
		t.Fatal("无法启动刮削")
	}
	var firstID atomic.Int64
	var searches atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/movies/search" {
			if searches.Add(1) == 2 {
				app.scrapeTaskMu.RLock()
				cancel := app.scrapeCancel
				app.scrapeTaskMu.RUnlock()
				cancel()
				http.Error(w, "cancel", http.StatusServiceUnavailable)
				return
			}
			number := r.URL.Query().Get("q")
			id, err := app.db.MovieIDByPath(filepath.Join(media, number+".strm"))
			if err != nil {
				t.Error(err)
			}
			firstID.Store(id)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": number, "number": number, "provider": "test", "title": "Updated"}}})
			return
		}
		number := filepath.Base(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": number, "number": number, "provider": "test", "title": "Updated"}})
	}))
	defer backend.Close()
	cfg := scraper.Config{BaseURL: backend.URL, Token: "test", Concurrency: 1, Overwrite: true}
	err = app.executeScrape(cfg, scraper.RunOptions{LibraryID: lib.ID, Overwrite: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("任务应取消: %v", err)
	}
	movie, err := app.db.Movie(firstID.Load())
	if err != nil || movie.Title != movie.Number+" Updated" {
		t.Fatalf("已成功NFO必须进入索引: %+v, %v", movie, err)
	}
	if app.scrapeStatus.Success != 1 || !app.scrapeStatus.Cancelled {
		t.Fatalf("任务状态: %+v", app.scrapeStatus)
	}
}
