package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"

	"github.com/gin-gonic/gin"
)

// 本文件是 P2-2（posterTag 每条目 2~3 次全局锁 + time.Now）与
// P2-3（requestedFields 每次请求多次扫描 ?Fields=）的量化基准。
//
// 两个热点都在"每条目几十纳秒"量级，必须和整页 0.5ms 的预算对照才有意义，
// 所以基准按"一页 200 条"记账（b.N 是一页，不是一次调用）。

// tagsBenchFixture 造一个 tag 缓存已经预热好的 App：benchmark 只测
// 命中路径的锁 + 时钟 + map 查找，不含 os.Stat（磁盘不在讨论范围）。
func tagsBenchFixture(b *testing.B) (*App, []string) {
	b.Helper()
	dir := b.TempDir()
	a, err := newApp(config.Config{DBPath: filepath.Join(dir, "tags-bench.db")}, cache.NewMemory(64))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { a.db.Close() })

	// 200 条影片 × 3 个图片路径，路径指向真实存在的空文件，
	// 这样万一缓存没命中会真的 stat（不会误把磁盘开销算进 hit 路径）。
	a.tags = make(map[string]tagEntry)
	paths := make([]string, 0, 600)
	for i := range 200 {
		for _, suffix := range []string{"poster", "landscape", "backdrop"} {
			p := filepath.Join(dir, suffix+string(rune('a'+i%26))+string(rune('a'+i/26)))
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				b.Fatal(err)
			}
			a.tags[p] = tagEntry{tag: "tag-hit", ts: time.Now()}
			paths = append(paths, p)
		}
	}
	return a, paths
}

// BenchmarkPosterTagHitPerPage 现在的形状：每条影片 3 次 posterTag 调用，
// 每次各自 time.Now() + tagMu 加锁/解锁。b.N 是一页（200 条 / 600 次调用）。
func BenchmarkPosterTagHitPerPage(b *testing.B) {
	a, paths := tagsBenchFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, p := range paths {
			if tag := a.posterTag(p); tag != "tag-hit" {
				b.Fatalf("unexpected tag %q", tag)
			}
		}
	}
}

// BenchmarkRequestedFields 现在的形状：一次请求解析一次 ?Fields=（6 个字段名判断，
// 每个都在原始串上扫一遍）。b.N 是一次请求。
func BenchmarkRequestedFields(b *testing.B) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/Items?Fields=MediaSources,DateCreated,Path,Overview,People,ProviderIds", nil)
	c.Request = req
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if f := requestedFields(c); !f.mediaSources || !f.dateCreated || !f.path {
			b.Fatal("fields not parsed")
		}
	}
}

// BenchmarkImageTagsForPage 是改造后的形状：每条影片一次 imageTagsFor 调用
// （一次时钟 + 一次加锁读 + 一次加锁写），取代 3 次 posterTag。
// b.N 是一页（200 条），与 BenchmarkPosterTagHitPerPage 可直接对比。
func BenchmarkImageTagsForPage(b *testing.B) {
	a, paths := tagsBenchFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < len(paths); j += 3 {
			primary, thumb, backdropTag := a.imageTagsFor(paths[j], paths[j+1], paths[j+2])
			if primary != "tag-hit" || thumb != "tag-hit" || backdropTag != "tag-hit" {
				b.Fatalf("unexpected tags %q/%q/%q", primary, thumb, backdropTag)
			}
		}
	}
}
