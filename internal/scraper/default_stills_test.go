package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
	"emby-go/internal/translate"

	"github.com/deepteams/webp"
)

func TestDefaultAllStillsAcrossScrapePaths(t *testing.T) {
	urls := make([]string, 30)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://images.example/still%d.jpg", i+1)
	}
	for _, mode := range []string{"apply", "manual", "batch", "skip existing", "legacy JPEG", "selected", "explicit empty", "images off", "no stills"} {
		t.Run(mode, func(t *testing.T) {
			info := metatube.MovieInfo{Provider: "p", ID: "id", Number: "ABC-123", Title: "film"}
			// Default downloads normalize whitespace/duplicates, preserving upstream order.
			info.PreviewImages = append([]string{" ", "  " + urls[0] + "  ", urls[0]}, urls[1:]...)
			if mode == "no stills" {
				info.PreviewImages = nil
			}
			requests := make(chan string, 64)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/movies/p/id" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"data": info})
					return
				}
				remote := r.URL.Query().Get("url")
				if r.URL.Path != "/v1/images/backdrop/p/id" || remote == "" {
					t.Errorf("unexpected request: %s", r.URL)
					http.Error(w, "unexpected", http.StatusBadRequest)
					return
				}
				requests <- remote
				_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 16, 9)), nil)
			}))
			defer backend.Close()
			cfg := DefaultConfig()
			cfg.BaseURL, cfg.Token, cfg.Translate = backend.URL, "test", translate.Config{}
			cfg.DownloadImages = mode != "images off"
			s := New(cfg, nil)
			dir := t.TempDir()
			movie := store.Movie{ProviderID: "p:id", Number: "ABC-123", SourcePath: filepath.Join(dir, "ABC-123.strm"), OutputDir: dir}
			for _, target := range imageTargets {
				if err := os.WriteFile(filepath.Join(dir, target.name), []byte("existing"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			opts := ApplyOptions{}
			want := urls
			switch mode {
			case "selected":
				opts.StillURLs = []string{urls[2], urls[0]}
				want = opts.StillURLs
			case "explicit empty":
				opts.StillURLs = []string{}
				want = nil
			case "images off", "no stills":
				want = nil
			case "legacy JPEG":
				extra := filepath.Join(dir, "extrafanart")
				if err := os.Mkdir(extra, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(extra, "fanart1.jpg"), []byte("legacy"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "skip existing":
				extra := filepath.Join(dir, "extrafanart")
				if err := os.Mkdir(extra, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(extra, "fanart1.webp"), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
				want = urls[1:]
			}
			switch mode {
			case "manual":
				if _, err := s.ScrapeOneMovie(context.Background(), movie, metatube.ProviderID{Provider: "p", ID: "id"}, false); err != nil {
					t.Fatal(err)
				}
			case "batch":
				if result := s.scrapeOne(context.Background(), movie, RunOptions{}); result.Status != StatusSuccess {
					t.Fatalf("batch = %+v", result)
				}
			default:
				result, err := s.Apply(context.Background(), movie, info, opts)
				if err != nil || len(result.ImageWarn) != 0 || result.ImageCount != len(want) {
					t.Fatalf("Apply = %+v, %v", result, err)
				}
			}
			var got []string
			for len(requests) > 0 {
				got = append(got, <-requests)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("requested stills = %v, want %v", got, want)
			}
			for i := range want {
				index := i + 1
				if mode == "skip existing" {
					index++
				}
				path := filepath.Join(dir, "extrafanart", fmt.Sprintf("fanart%d.webp", index))
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				_, decodeErr := webp.Decode(file)
				_ = file.Close()
				if decodeErr != nil {
					t.Fatalf("not WebP: %s: %v", path, decodeErr)
				}
			}
			if mode == "skip existing" {
				raw, err := os.ReadFile(filepath.Join(dir, "extrafanart", "fanart1.webp"))
				if err != nil || string(raw) != "keep" {
					t.Fatalf("existing still changed: %q, %v", raw, err)
				}
			}
			if mode == "legacy JPEG" {
				raw, err := os.ReadFile(filepath.Join(dir, "extrafanart", "fanart1.jpg"))
				if err != nil || string(raw) != "legacy" {
					t.Fatalf("legacy JPEG changed: %q, %v", raw, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "ABC-123.nfo")); err != nil {
				t.Fatal(err)
			}
			if len(want) == 0 {
				if _, err := os.Stat(filepath.Join(dir, "extrafanart")); !os.IsNotExist(err) {
					t.Fatalf("unexpected still directory: %v", err)
				}
			}
		})
	}
}
