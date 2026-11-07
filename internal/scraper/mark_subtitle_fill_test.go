package scraper

import (
	"context"
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

// 「只补缺失」模式下，用户自定义分类与人工标签不能被外挂字幕标记改写：
// 标签列表非空时块级替换只在覆盖式刮削里发生；补缺失模式只填空字段，
// 因此既有 <tag> 原样保留（代价是中文字幕标记也不会新增），
// 而主海报仍按目录里的中文字幕贴 zimu 角标。
func TestApplyFillOnlyKeepsCustomCategoryAndManualTags(t *testing.T) {
	var mu sync.Mutex
	badge := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if kind, ok := cutImageKind(r.URL.Path); ok {
			if kind == "primary" {
				mu.Lock()
				badge = r.URL.Query().Get("badge")
				mu.Unlock()
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"nope"}}`))
	}))
	t.Cleanup(backend.Close)

	dir := t.TempDir()
	source := filepath.Join(dir, "ABF-018.strm")
	if err := os.WriteFile(source, []byte("http://example.invalid/v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ABF-018.zh.srt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(dir, "ABF-018.nfo")
	existing := `<movie><title>人工标题</title><genre>自建分类</genre><tag>人工标签</tag><plot>人工简介</plot></movie>`
	if err := os.WriteFile(nfoPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	movie := store.Movie{ID: 1, LibraryID: 1, SourcePath: source, OutputDir: dir, NFOPath: nfoPath,
		Number: "ABF-018", Status: "success"}
	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	scraper.cfg.Translate = translate.Config{}
	info := metatube.MovieInfo{ID: "abc", Provider: "fanza", Number: "ABF-018", Title: "上游标题",
		Genres: []string{"剧情"}, Label: "厂牌A"}

	if _, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{Provider: "fanza", ID: "abc"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{"人工标题", "自建分类", "人工标签", "人工简介", "<num>ABF-018</num>", "<label>厂牌A</label>"} {
		if !strings.Contains(got, want) {
			t.Errorf("NFO 丢了 %q；NFO:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<tag>中文字幕</tag>") {
		t.Errorf("只补缺失不该新增标签；NFO:\n%s", got)
	}
	mu.Lock()
	gotBadge := badge
	mu.Unlock()
	if gotBadge != "zimu.png" {
		t.Errorf("主海报角标 = %q，期望 zimu.png（目录里有中文字幕）", gotBadge)
	}
}
