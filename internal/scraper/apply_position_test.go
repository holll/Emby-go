package scraper

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
	"emby-go/internal/translate"
)

// MT-02: 标记里的裁切位置只作用于主海报；另两张宽幅图传了也是空转，必须不传。
func TestApplyPositionCropsOnlyPrimaryPoster(t *testing.T) {
	var mu sync.Mutex
	positions := map[string]string{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, ok := cutImageKind(r.URL.Path)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"nope"}}`))
			return
		}
		mu.Lock()
		positions[kind] = r.URL.Query().Get("pos")
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	}))
	t.Cleanup(backend.Close)

	dir := t.TempDir()
	movie := store.Movie{
		ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "ABF-018.strm"),
		OutputDir: dir, NFOPath: filepath.Join(dir, "ABF-018.nfo"), Number: "ABF-018", Status: "pending",
	}
	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	scraper.cfg.Translate = translate.Config{}
	info := metatube.MovieInfo{ID: "abc", Provider: "fanza", Number: "ABF-018", Title: "原文标题"}

	position := 0.3
	if _, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{
		Provider: "fanza", ID: "abc", Position: &position, Overwrite: true,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	mu.Lock()
	primary, backdrop, thumb := positions["primary"], positions["backdrop"], positions["thumb"]
	mu.Unlock()
	if primary != "0.3" {
		t.Errorf("主海报应带 pos=0.3: %q", primary)
	}
	if backdrop != "" || thumb != "" {
		t.Errorf("宽幅图不应带 pos: backdrop=%q thumb=%q", backdrop, thumb)
	}

	// 无 Position 时保持旧请求：三张图都不带 pos。
	positions = map[string]string{}
	if _, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{
		Provider: "fanza", ID: "abc", Overwrite: true,
	}); err != nil {
		t.Fatalf("Apply without position: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for kind, got := range positions {
		if got != "" {
			t.Errorf("%s 不应带 pos: %q", kind, got)
		}
	}
}

// MT-02: 已存标记的 Position 要随候选带到 inspect，并在确认路径上真正影响主海报。
func TestStoredIdentityPositionCarriedToPreviewAndApply(t *testing.T) {
	var mu sync.Mutex
	imagePos := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if kind, ok := cutImageKind(r.URL.Path); ok {
			mu.Lock()
			imagePos[kind] = r.URL.Query().Get("pos")
			mu.Unlock()
			w.Header().Set("Content-Type", "image/jpeg")
			_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"precise film"}}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	movie := store.Movie{
		ID: 42, ProviderID: "FANZA:abc00123:0.3", Number: "OLD-001",
		OutputDir: dir, SourcePath: filepath.Join(dir, "OLD-001.strm"), NFOPath: filepath.Join(dir, "movie.nfo"),
	}
	cfg := DefaultConfig()
	cfg.BaseURL, cfg.Token = ts.URL, "test"
	cfg.Translate = translate.Config{}
	scraper := New(cfg, nil)

	preview, err := scraper.Preview(context.Background(), movie)
	if err != nil || len(preview.Candidates) != 1 {
		t.Fatalf("preview=%+v error=%v", preview, err)
	}
	if pos := preview.Candidates[0].Position; pos == nil || *pos != 0.3 {
		t.Fatalf("候选应带回裁切位置: %+v", preview.Candidates[0])
	}
	// 后缀不进影片身份。
	if preview.Candidates[0].ID != "abc00123" {
		t.Errorf("候选 ID 不应带后缀: %q", preview.Candidates[0].ID)
	}

	pid, ok := ParseStoredProviderID(movie.ProviderID)
	if !ok {
		t.Fatal("marker should parse")
	}
	result, err := scraper.ScrapeOneMovie(context.Background(), movie, pid, true)
	if err != nil || result.Number != "NEW-002" {
		t.Fatalf("ScrapeOneMovie=%+v error=%v", result, err)
	}
	mu.Lock()
	primary, thumb := imagePos["primary"], imagePos["thumb"]
	mu.Unlock()
	if primary != "0.3" {
		t.Errorf("确认写入的主海报应带 pos=0.3: %q", primary)
	}
	if thumb != "" {
		t.Errorf("landscape 不应带 pos: %q", thumb)
	}
	nfo, err := os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := providerID("FANZA", "abc00123"); !strings.Contains(string(nfo), want) {
		t.Errorf("NFO 身份应为 %q: %s", want, nfo)
	}
	if strings.Contains(string(nfo), ":0.3") {
		t.Errorf("裁切位置不应写进 NFO 身份: %s", nfo)
	}
}

// cutImageKind 从 /v1/images/{kind}/{provider}/{id} 里取图片类型。
func cutImageKind(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/v1/images/")
	if !ok {
		return "", false
	}
	kind, _, ok := strings.Cut(rest, "/")
	return kind, ok
}
