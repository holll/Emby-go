package scraper

import (
	"bytes"
	"context"
	"errors"
	"github.com/deepteams/webp"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
	"emby-go/internal/translate"
)

func TestApplySelectedStillsWebPOrderAndTail(t *testing.T) {
	dir := t.TempDir()
	movie := store.Movie{SourcePath: filepath.Join(dir, "movie.strm"), OutputDir: dir}
	requests := make(chan string, 16)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote := r.URL.Query().Get("url")
		if remote != "" {
			if r.URL.Path != "/v1/images/backdrop/provider/id" || r.URL.Query().Get("quality") != "79" || r.URL.Query().Has("pos") || r.URL.Query().Has("ratio") || r.URL.Query().Has("badge") {
				t.Errorf("selected still request = %s", r.URL)
			}
		}
		requests <- r.URL.Path + "|" + remote
		if _, err := os.Stat(filepath.Join(dir, "movie.nfo")); !os.IsNotExist(err) {
			t.Errorf("NFO must be written after images: %v", err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 32, 18))
		shade := uint8(20)
		if strings.HasSuffix(remote, "second") {
			shade = 220
		}
		for y := range 18 {
			for x := range 32 {
				img.SetRGBA(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
			}
		}
		_ = png.Encode(w, img)
	}))
	defer backend.Close()
	cfg := DefaultConfig()
	cfg.BaseURL, cfg.Token, cfg.ImageQuality, cfg.Translate = backend.URL, "test", 79, translate.Config{}
	s := New(cfg, nil)
	extra := filepath.Join(dir, "extrafanart")
	if err := os.Mkdir(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "fanart1.webp"), []byte("old first"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := filepath.Join(extra, "fanart3.webp")
	if err := os.WriteFile(tail, []byte("old tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	urls := []string{"https://trusted/first", "https://trusted/second"}
	result, err := s.Apply(context.Background(), movie, metatube.MovieInfo{Provider: "provider", ID: "id", Title: "new"}, ApplyOptions{Overwrite: true, StillURLs: urls})
	if err != nil || len(result.ImageWarn) != 0 || result.ImageCount != 5 {
		t.Fatalf("Apply = %+v, %v", result, err)
	}
	wantPaths := []string{filepath.Join(dir, "poster.webp"), filepath.Join(dir, "fanart.webp"), filepath.Join(dir, "landscape.webp"), filepath.Join(extra, "fanart1.webp"), filepath.Join(extra, "fanart2.webp")}
	if !reflect.DeepEqual(result.Images, wantPaths) {
		t.Errorf("written paths = %v, want %v", result.Images, wantPaths)
	}
	wantRequests := []string{"/v1/images/primary/provider/id|", "/v1/images/backdrop/provider/id|", "/v1/images/thumb/provider/id|", "/v1/images/backdrop/provider/id|" + urls[0], "/v1/images/backdrop/provider/id|" + urls[1]}
	for _, want := range wantRequests {
		if got := <-requests; got != want {
			t.Errorf("request = %q, want %q", got, want)
		}
	}
	for i, path := range wantPaths[3:] {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		img, err := webp.Decode(bytes.NewReader(raw))
		if err != nil || img.Bounds() != image.Rect(0, 0, 32, 18) {
			t.Fatalf("real 16:9 WebP %s: %v", path, err)
		}
		r, _, _, _ := img.At(0, 0).RGBA()
		if (i == 0 && r > 100*257) || (i == 1 && r < 100*257) {
			t.Errorf("selected order wrong at %s: shade=%d", path, r/257)
		}
	}
	if got, err := os.ReadFile(tail); err != nil || string(got) != "old tail" {
		t.Errorf("tail changed: %q, %v", got, err)
	}
}

func TestApplySelectedStillsSkipAndDisabled(t *testing.T) {
	for _, mode := range []string{"skip", "images off", "nil", "empty"} {
		t.Run(mode, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected image request: %s", r.URL)
				http.Error(w, "unexpected", http.StatusInternalServerError)
			}))
			defer backend.Close()
			cfg := DefaultConfig()
			cfg.BaseURL, cfg.Token, cfg.Translate = backend.URL, "test", translate.Config{}
			cfg.DownloadImages = mode != "images off"
			s := New(cfg, nil)
			dir := t.TempDir()
			for _, target := range imageTargets {
				if err := os.WriteFile(filepath.Join(dir, target.name), []byte("existing"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			extra := filepath.Join(dir, "extrafanart")
			opts := ApplyOptions{StillURLs: []string{"https://trusted/first"}}
			if mode == "skip" {
				if err := os.Mkdir(extra, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(extra, "fanart1.webp"), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if mode == "nil" {
				opts.StillURLs = nil
			} else if mode == "empty" {
				opts.StillURLs = []string{}
			}
			result, err := s.Apply(context.Background(), store.Movie{SourcePath: filepath.Join(dir, "movie.strm")}, metatube.MovieInfo{Title: "new"}, opts)
			if err != nil || result.ImageCount != 0 || len(result.Images) != 0 || len(result.ImageWarn) != 0 {
				t.Fatalf("Apply = %+v, %v", result, err)
			}
			if mode == "skip" {
				if got, err := os.ReadFile(filepath.Join(extra, "fanart1.webp")); err != nil || string(got) != "old" {
					t.Errorf("skip changed old image: %q, %v", got, err)
				}
			} else if _, err := os.Stat(extra); !os.IsNotExist(err) {
				t.Errorf("unexpected extra directory: %v", err)
			}
		})
	}
}

func TestApplySelectedStillsFailurePreservesTargetsAndContinues(t *testing.T) {
	for _, failure := range []string{"download", "encode", "rename"} {
		t.Run(failure, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("url") == "https://trusted/first" {
					switch failure {
					case "download":
						http.Error(w, "upstream failed", http.StatusBadGateway)
						return
					case "encode":
						_, _ = w.Write([]byte("not an image"))
						return
					}
				}
				_ = png.Encode(w, image.NewRGBA(image.Rect(0, 0, 16, 9)))
			}))
			defer backend.Close()
			cfg := DefaultConfig()
			cfg.BaseURL, cfg.Token, cfg.Translate = backend.URL, "test", translate.Config{}
			s := New(cfg, nil)
			dir := t.TempDir()
			extra := filepath.Join(dir, "extrafanart")
			if err := os.Mkdir(extra, 0o755); err != nil {
				t.Fatal(err)
			}
			first := filepath.Join(extra, "fanart1.webp")
			oldPath := first
			if failure == "rename" {
				if err := os.Mkdir(first, 0o755); err != nil {
					t.Fatal(err)
				}
				oldPath = filepath.Join(first, "retained")
			}
			if err := os.WriteFile(oldPath, []byte("old target"), 0o644); err != nil {
				t.Fatal(err)
			}
			sentinel := first + ".tmp"
			if err := os.WriteFile(sentinel, []byte("other request"), 0o644); err != nil {
				t.Fatal(err)
			}
			result, err := s.Apply(context.Background(), store.Movie{SourcePath: filepath.Join(dir, "movie.strm")}, metatube.MovieInfo{Provider: "p", ID: "i", Title: "new"}, ApplyOptions{Overwrite: true, StillURLs: []string{"https://trusted/first", "https://trusted/second"}})
			if err != nil || result.ImageCount != 4 || len(result.ImageWarn) != 1 || !strings.HasPrefix(result.ImageWarn[0], "extrafanart/fanart1.webp:") {
				t.Fatalf("Apply = %+v, %v", result, err)
			}
			if got, err := os.ReadFile(oldPath); err != nil || string(got) != "old target" {
				t.Errorf("old target lost: %q, %v", got, err)
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "other request" {
				t.Errorf("other temp changed: %q, %v", got, err)
			}
			entries, err := os.ReadDir(extra)
			if err != nil || len(entries) != 3 {
				t.Errorf("temporary files leaked: %v, %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "movie.nfo")); err != nil {
				t.Errorf("NFO not written after image warning: %v", err)
			}
		})
	}
}

func TestApplySelectedStillsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan string, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Query().Get("url")
		cancel()
		http.Error(w, "canceled", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	cfg := DefaultConfig()
	cfg.BaseURL, cfg.Token, cfg.Translate = backend.URL, "test", translate.Config{}
	s := New(cfg, nil)
	dir := t.TempDir()
	for _, target := range imageTargets {
		if err := os.WriteFile(filepath.Join(dir, target.name), []byte("existing"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Apply(ctx, store.Movie{SourcePath: filepath.Join(dir, "movie.strm")}, metatube.MovieInfo{Provider: "p", ID: "i", Title: "new"}, ApplyOptions{StillURLs: []string{"https://trusted/first", "https://trusted/second"}})
	if !errors.Is(err, context.Canceled) || len(result.ImageWarn) != 0 || result.ImageCount != 0 {
		t.Fatalf("Apply = %+v, %v", result, err)
	}
	if len(requests) != 1 || <-requests != "https://trusted/first" {
		t.Error("cancellation did not stop selected stills")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.nfo")); !os.IsNotExist(err) {
		t.Errorf("cancellation wrote NFO: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extrafanart")); !os.IsNotExist(err) {
		t.Errorf("cancellation wrote extra images: %v", err)
	}
}
