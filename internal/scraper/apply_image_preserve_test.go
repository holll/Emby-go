package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/store"
	"emby-go/internal/translate"
)

// 上游图片失败时不能碰已有图片：覆盖式重刮的下图失败必须保留上一次成功落盘的
// poster.webp 字节，否则一次临时上游故障就会把整库海报清空。
func TestApplyImageFailureKeepsExistingImage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/images/") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":500,"message":"upstream down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"abc","provider":"fanza","number":"ABF-018","title":"T",
			"summary":"S","genres":["剧情"],"cover_url":"https://c/1.jpg"}}`))
	}))
	defer ts.Close()
	dir := t.TempDir()
	movie := store.Movie{ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "x.strm"),
		OutputDir: dir, Number: "ABF-018", Status: "success"}
	poster := filepath.Join(dir, "poster.webp")
	const old = "OLD-POSTER-BYTES"
	if err := os.WriteFile(poster, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	scraper := newTestScraper(t, ts.URL, DefaultConfig())
	scraper.cfg.Translate = translate.Config{}
	info, _ := scraper.MovieInfo(context.Background(), "fanza", "abc")
	result, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{Provider: "fanza", ID: "abc", Overwrite: true})
	if err != nil {
		t.Fatalf("图片失败不应让刮削整体失败: %v", err)
	}
	if len(result.ImageWarn) != 3 {
		t.Errorf("应记录 3 条图片 warn: %+v", result)
	}
	got, err := os.ReadFile(poster)
	if err != nil {
		t.Fatalf("已有海报不应被删除: %v", err)
	}
	if string(got) != old {
		t.Errorf("已有海报被覆盖: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.nfo")); err != nil {
		t.Errorf("元数据仍应落盘: %v", err)
	}
}
