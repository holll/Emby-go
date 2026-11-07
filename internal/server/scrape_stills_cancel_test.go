package server

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Cancel while the fresh selected-still detail lookup is actually in flight.
// Exercise the real router/auth/handler with a cancellable request; only MetaTube
// is a fixture. Waiting for ServeHTTP to return also observes the deferred gate release.
func TestScrapeSelectedStillsCanceledLookup(t *testing.T) {
	app, _, token := contractEnv(t)
	entered, upstreamCanceled := make(chan struct{}), make(chan struct{})
	var downloads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/movies/test/cancel" {
			close(entered)
			<-r.Context().Done()
			close(upstreamCanceled)
			return
		}
		downloads.Add(1)
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	before, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	version := app.db.Version("g:version")
	snapshot := func() map[string][]byte {
		t.Helper()
		files := make(map[string][]byte)
		err := filepath.WalkDir(filepath.Dir(before.SourcePath), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = data
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	files := snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/items/1/scrape", strings.NewReader(`{"provider":"test","id":"cancel","stills":{"version":"`+strings.Repeat("a", 64)+`","indices":[0]}}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Emby-Token", token)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	handler := app.Handler()
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("selected confirmation never reached fresh lookup")
	}
	if app.nfoBusyOwner() == "" {
		t.Error("fresh lookup must hold the NFO gate")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled confirmation did not finish")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not reach upstream")
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("canceled lookup status=%d body=%s", response.Code, response.Body.String())
	}
	if owner := app.nfoBusyOwner(); owner != "" {
		t.Fatalf("NFO gate leaked: %q", owner)
	}
	if !app.claimNFO("edit") {
		t.Fatal("subsequent edit cannot acquire released NFO gate")
	}
	app.releaseNFO("edit")
	after, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || app.db.Version("g:version") != version {
		t.Fatalf("canceled lookup changed movie or published a version: before=%+v after=%+v", before, after)
	}
	afterFiles := snapshot()
	if len(files) != len(afterFiles) {
		t.Fatalf("canceled lookup changed file set: before=%v after=%v", files, afterFiles)
	}
	for path, data := range files {
		if !bytes.Equal(data, afterFiles[path]) {
			t.Errorf("canceled lookup changed %s", path)
		}
	}
	if downloads.Load() != 0 {
		t.Fatalf("canceled lookup made %d image requests", downloads.Load())
	}
}
