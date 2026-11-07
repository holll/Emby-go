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

// MT-04：文件名没有版本标记时，同目录外挂字幕的语言段决定是否算中文字幕。
func TestVersionMarkForExternalSubtitle(t *testing.T) {
	cases := []struct {
		name     string
		video    string
		siblings []string
		want     string
	}{
		{name: "无字幕", video: "ABF-018.strm", want: ""},
		{name: "中文语言段", video: "ABF-018.strm", siblings: []string{"ABF-018.zh.srt"}, want: "C"},
		{name: "简繁写法", video: "ABF-018.strm", siblings: []string{"ABF-018.简体.ass"}, want: "C"},
		{name: "大写扩展名", video: "ABF-018.strm", siblings: []string{"ABF-018.zh-CN.SRT"}, want: "C"},
		{name: "英文不算", video: "ABF-018.strm", siblings: []string{"ABF-018.en.srt"}, want: ""},
		{name: "裸字幕语言未知", video: "ABF-018.strm", siblings: []string{"ABF-018.srt"}, want: ""},
		{name: "主名不同不算", video: "ABF-018.strm", siblings: []string{"ABF-0180.zh.srt"}, want: ""},
		{name: "非字幕扩展名不算", video: "ABF-018.strm", siblings: []string{"ABF-018.zh.txt"}, want: ""},
		{name: "文件名标记优先", video: "ABF-018-U.strm", siblings: []string{"ABF-018-U.zh.srt"}, want: "U"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, tc.video)
			if err := os.WriteFile(source, []byte("http://example.invalid/v"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, sibling := range tc.siblings {
				if err := os.WriteFile(filepath.Join(dir, sibling), []byte("1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			mark, ok := versionMarkFor(source)
			if tc.want == "" {
				if ok {
					t.Fatalf("期望无标记，得到 %q", mark.Code)
				}
				return
			}
			if !ok || mark.Code != tc.want {
				t.Fatalf("期望 %q，得到 %q（ok=%v）", tc.want, mark.Code, ok)
			}
		})
	}
}

// 目录不可读时只当作「没有字幕」，不能让刮削失败。
func TestHasChineseSubtitleMissingDir(t *testing.T) {
	if hasChineseSubtitle(filepath.Join(t.TempDir(), "gone", "ABF-018.strm")) {
		t.Fatal("目录不存在时应返回 false")
	}
}

// 端到端：只有外挂中文字幕时，刮削要写「中文字幕」标签并给主海报贴 zimu 角标；
// 只有英文字幕时两者都不写。
func TestApplyChineseSubtitleMark(t *testing.T) {
	cases := []struct {
		name      string
		subtitle  string
		wantBadge string
		wantTag   bool
	}{
		{name: "中文字幕", subtitle: "ABF-018.chs.srt", wantBadge: "zimu.png", wantTag: true},
		{name: "英文字幕", subtitle: "ABF-018.en.srt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			badges := map[string]string{}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				kind, ok := cutImageKind(r.URL.Path)
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":{"code":404,"message":"nope"}}`))
					return
				}
				mu.Lock()
				badges[kind] = r.URL.Query().Get("badge")
				mu.Unlock()
				w.Header().Set("Content-Type", "image/jpeg")
				_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
			}))
			t.Cleanup(backend.Close)

			dir := t.TempDir()
			source := filepath.Join(dir, "ABF-018.strm")
			if err := os.WriteFile(source, []byte("http://example.invalid/v"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.subtitle), []byte("1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			nfoPath := filepath.Join(dir, "ABF-018.nfo")
			movie := store.Movie{
				ID: 1, LibraryID: 1, SourcePath: source, OutputDir: dir, NFOPath: nfoPath,
				Number: "ABF-018", Status: "pending",
			}
			scraper := newTestScraper(t, backend.URL, DefaultConfig())
			scraper.cfg.Translate = translate.Config{}
			info := metatube.MovieInfo{ID: "abc", Provider: "fanza", Number: "ABF-018", Title: "原文标题"}

			if _, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{
				Provider: "fanza", ID: "abc", Overwrite: true,
			}); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			mu.Lock()
			primary, thumb := badges["primary"], badges["thumb"]
			mu.Unlock()
			if primary != tc.wantBadge {
				t.Errorf("主海报角标 = %q，期望 %q", primary, tc.wantBadge)
			}
			if thumb != "" {
				t.Errorf("thumb 不该贴角标，得到 %q", thumb)
			}

			raw, err := os.ReadFile(nfoPath)
			if err != nil {
				t.Fatalf("读 NFO: %v", err)
			}
			if got := strings.Contains(string(raw), "<tag>中文字幕</tag>"); got != tc.wantTag {
				t.Errorf("NFO 中文字幕标签存在=%v，期望 %v；NFO:\n%s", got, tc.wantTag, raw)
			}
		})
	}
}

// 字幕增删：真实数据库记录首次生成归属，重读影片后仅撤销已归属的标签。
// 人工标签必须跨两次刮削保留；无状态调用不能证明归属可撤回。
func TestApplyChineseSubtitleMarkFollowsFileChanges(t *testing.T) {
	var mu sync.Mutex
	badges := map[string]string{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, ok := cutImageKind(r.URL.Path)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"nope"}}`))
			return
		}
		mu.Lock()
		badges[kind] = r.URL.Query().Get("badge")
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	}))
	t.Cleanup(backend.Close)

	dir := t.TempDir()
	source := filepath.Join(dir, "ABF-018.strm")
	if err := os.WriteFile(source, []byte("http://example.invalid/v"), 0o644); err != nil {
		t.Fatal(err)
	}
	subtitle := filepath.Join(dir, "ABF-018.zh.srt")
	if err := os.WriteFile(subtitle, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(dir, "ABF-018.nfo")
	movie := store.Movie{
		ID: 1, LibraryID: 1, SourcePath: source, OutputDir: dir, NFOPath: nfoPath,
		Number: "ABF-018", Status: "pending",
	}
	if err := os.WriteFile(nfoPath, []byte("<movie><tag>手工</tag></movie>"), 0644); err != nil {
		t.Fatal(err)
	}
	db, indexed := autoMarksStore(t, source)
	movie = indexed
	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	scraper.store = db
	scraper.cfg.Translate = translate.Config{}
	info := metatube.MovieInfo{ID: "abc", Provider: "fanza", Number: "ABF-018", Title: "原文标题", Label: "厂牌A"}
	apply := func() string {
		t.Helper()
		if _, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{
			Provider: "fanza", ID: "abc", Overwrite: true,
		}); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		var err error
		movie, err = db.Movie(movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(nfoPath)
		if err != nil {
			t.Fatalf("读 NFO: %v", err)
		}
		if !strings.Contains(string(raw), "<tag>手工</tag>") {
			t.Fatalf("人工标签丢失: %s", raw)
		}
		return string(raw)
	}

	if got := apply(); !strings.Contains(got, "<tag>中文字幕</tag>") {
		t.Fatalf("首次刮削应写中文字幕标签：\n%s", got)
	}
	mu.Lock()
	first := badges["primary"]
	mu.Unlock()
	if first != "zimu.png" {
		t.Fatalf("首次刮削主海报应贴 zimu 角标，得到 %q", first)
	}

	if err := os.Remove(subtitle); err != nil {
		t.Fatal(err)
	}
	if got := apply(); strings.Contains(got, "<tag>中文字幕</tag>") {
		t.Fatalf("删除字幕后重刮不应残留标签：\n%s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if badges["primary"] != "" {
		t.Errorf("删除字幕后主海报不该再贴角标，得到 %q", badges["primary"])
	}
}
