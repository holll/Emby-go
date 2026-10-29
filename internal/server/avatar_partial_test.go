package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/avatar"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

func TestAvatarPartialWriteReachesTaskAndProgress(t *testing.T) {
	a, _, _ := authorityApp(t)
	seedOwner(t, a)
	root := t.TempDir()
	lib, err := a.db.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(root, "broken.nfo")
	if err = os.Mkdir(nfoPath, 0o700); err != nil {
		t.Fatal(err)
	}
	const name = "Fixture actor"
	_, err = a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "ABC-100.strm"), NFOPath: nfoPath, Status: "success"}, 1, time.Unix(1, 0), []store.ActorRef{{Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var backend *httptest.Server
	backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/picture.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(picture.Bytes())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "fixture", "name": name, "provider": "fixture", "images": []string{backend.URL + "/picture.png"}}}})
	}))
	defer backend.Close()
	if err := a.beginScrape("scrape_avatars", 1); err != nil {
		t.Fatalf("begin failed: %v", err)
	}
	dir := filepath.Join(root, "avatars")
	err = a.executeScrapeAvatars(scraper.Config{BaseURL: backend.URL, Token: "fixture", Concurrency: 1, AvatarsDir: dir}, lib.ID, 0)
	if !errors.Is(err, scraper.ErrAvatarWriteIncomplete) {
		t.Fatalf("task returned success: %v", err)
	}
	if _, err = os.Stat(avatar.Path(dir, name)); err != nil {
		t.Fatal("saved avatar missing", err)
	}
	a.taskMu.Lock()
	task := a.tasks[0]
	a.taskMu.Unlock()
	if task.Status != "failed" || !strings.Contains(task.Error, scraper.ErrAvatarWriteIncomplete.Error()) || strings.Contains(task.Error, root) {
		t.Fatalf("task %+v", task)
	}
	if err = a.db.SaveAccessToken("avatar-partial-token"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/scrape/progress", nil)
	req.Header.Set("X-Emby-Token", "avatar-partial-token")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	var status scrapeStatus
	if err = json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || status.Running || status.Failed != 1 || status.Success != 0 || status.Error != scraper.ErrAvatarWriteIncomplete.Error() || status.FinishedAt == "" || len(status.Failures) != 1 {
		t.Fatalf("progress %d %s %v", w.Code, w.Body.String(), err)
	}
	if owner := a.nfoBusyOwner(); owner != "" {
		t.Fatalf("gate leaked: %q", owner)
	}
	// Existing selection is URL-based, not a durable incomplete-work queue.
	// This limitation must remain explicit rather than promising automatic retry.
	names, err := a.db.ActorsMissingAvatar(lib.ID, 0)
	if err != nil || len(names) != 0 {
		t.Fatalf("unexpected automatic recovery selection: %v %v", names, err)
	}
}

func TestAvatarPartialWriteIsVisibleAlongsideCancellation(t *testing.T) {
	a, _, _ := authorityApp(t)
	if err := a.beginScrape("scrape_avatars", 1); err != nil {
		t.Fatalf("begin failed: %v", err)
	}
	a.scrapeTaskMu.RLock()
	cancel := a.scrapeCancel
	a.scrapeTaskMu.RUnlock()
	cancel()
	a.endScrape(errors.Join(context.Canceled, scraper.ErrAvatarWriteIncomplete))
	a.scrapeTaskMu.RLock()
	status := a.scrapeStatus
	a.scrapeTaskMu.RUnlock()
	if !status.Cancelled || status.Running || status.Error != scraper.ErrAvatarWriteIncomplete.Error() {
		t.Fatalf("cancellation hid saved output: %+v", status)
	}
	if err := a.beginScrape("scrape_avatars", 0); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	a.endScrape(nil)
	a.scrapeTaskMu.RLock()
	status = a.scrapeStatus
	a.scrapeTaskMu.RUnlock()
	if status.Error != "" || status.Cancelled {
		t.Fatalf("next task retained stale error: %+v", status)
	}
}
