package scraper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
	"emby-go/internal/translate"
)

func TestApplyCanceledDoesNotWriteNFO(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 已取消的调用必须在入口停止，不应访问客户端。
	s := &Scraper{}
	_, err := s.Apply(ctx, store.Movie{SourcePath: filepath.Join(dir, "movie.strm")}, metatube.MovieInfo{Title: "new"}, ApplyOptions{Overwrite: true})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Apply error = %v, want context.Canceled", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("canceled Apply wrote files: %v", entries)
	}
}

func TestApplyCancellationDuringExternalRequest(t *testing.T) {
	for _, stage := range []string{"translate", "image"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				cancel()
				http.Error(w, "canceled", http.StatusServiceUnavailable)
			}))
			defer backend.Close()
			cfg := DefaultConfig()
			cfg.Translate = translate.Config{}
			if stage == "translate" {
				cfg.Translate = translate.Config{APIURL: backend.URL, APIKey: "test"}
				cfg.TranslateTitle, cfg.TranslatePlot = true, true
			}
			s := newTestScraper(t, backend.URL, cfg)
			s.cfg.DownloadImages = stage == "image"
			dir := t.TempDir()
			path := filepath.Join(dir, "movie.nfo")
			original := []byte("<movie><title>existing title</title><plot>existing plot</plot></movie>")
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatal(err)
			}
			movie := store.Movie{SourcePath: filepath.Join(dir, "movie.strm"), NFOPath: path, OutputDir: dir}
			result, err := s.Apply(ctx, movie, metatube.MovieInfo{Provider: "p", ID: "1", Title: "original", Summary: "original plot"}, ApplyOptions{Overwrite: true})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Apply error = %v, want context.Canceled", err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(raw) != string(original) {
				t.Errorf("cancellation overwrote existing NFO: %s", raw)
			}
			if requests.Load() != 1 {
				t.Errorf("requests = %d, want 1", requests.Load())
			}
			if len(result.ImageWarn) != 0 {
				t.Errorf("cancellation must not become an image warning: %v", result.ImageWarn)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("unexpected files after cancellation: %v", entries)
			}
		})
	}
}

func TestDownloadImageConcurrentUniqueTemp(t *testing.T) {
	backend := fakeBackend(t)
	s := newTestScraper(t, backend.URL, DefaultConfig())
	dir := t.TempDir()
	dest := filepath.Join(dir, "poster.webp")
	// 其他请求占用的临时文件不能被覆盖或删除。
	sentinel := []byte("another request owns this file")
	if err := os.WriteFile(dest+".tmp", sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	const workers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.downloadImage(context.Background(), metatube.MovieInfo{Provider: "p", ID: "1"}, "primary", dest, "", true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent download: %v", err)
		}
	}
	raw, err := os.ReadFile(dest + ".tmp")
	if err != nil || string(raw) != string(sentinel) {
		t.Errorf("another request's temp file changed: %q, %v", raw, err)
	}
	raw, err = os.ReadFile(dest)
	if err != nil || len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		t.Errorf("invalid saved WebP: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("temporary files leaked: %v", entries)
	}
}

func TestDownloadImageFailureCleansOnlyOwnTemp(t *testing.T) {
	for _, failure := range []string{"encode", "rename"} {
		t.Run(failure, func(t *testing.T) {
			backend := fakeBackend(t)
			if failure == "encode" {
				backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not an image")) }))
				defer backend.Close()
			}
			s := newTestScraper(t, backend.URL, DefaultConfig())
			dir := t.TempDir()
			dest := filepath.Join(dir, "poster.webp")
			if failure == "rename" {
				if err := os.Mkdir(dest, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := []byte("owned by another request")
			if err := os.WriteFile(dest+".tmp", sentinel, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := s.downloadImage(context.Background(), metatube.MovieInfo{Provider: "p", ID: "1"}, "primary", dest, "", true); err == nil {
				t.Fatal("expected save failure")
			}
			raw, err := os.ReadFile(dest + ".tmp")
			if err != nil || string(raw) != string(sentinel) {
				t.Errorf("another request's temp file changed: %q, %v", raw, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if failure == "rename" {
				want++
			}
			if len(entries) != want {
				t.Errorf("temporary files leaked: %v", entries)
			}
		})
	}
}
