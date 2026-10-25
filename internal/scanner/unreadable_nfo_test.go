package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/store"
)

// NFO 存在但读不出来（截断、非法 XML）不是「清空索引」的指令：
// 已索引的元数据与可见状态必须保留，原因写进 last_scrape_error 并计入
// unreadable_nfo；修好 NFO 后下一次扫描要能自动恢复。
func TestScanPreservesIndexedMetadataWhenNFOUnreadable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "ABC-001.strm")
	nfoPath := filepath.Join(root, "ABC-001.nfo")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "https://media.test/a.mp4\n")
	write(nfoPath, "<movie><title>原始标题</title><plot>原始简介</plot></movie>")

	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	id, err := s.MovieIDByPath(source)
	if err != nil || id == 0 {
		t.Fatalf("取影片 id 失败: %d %v", id, err)
	}
	if got, err := s.Movie(id); err != nil || got.Title != "原始标题" || got.Status != "success" {
		t.Fatalf("首次扫描应正常入库: %+v %v", got, err)
	}

	modes := []struct {
		name string
		scan func() (Result, error)
	}{
		{"incremental", func() (Result, error) {
			return ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
		}},
		{"full", func() (Result, error) { return Scan(s, lib) }},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			// 截断的 XML：解析必然失败，但文件确实存在。
			write(nfoPath, "<movie><title>被截断的标题")
			result, err := mode.scan()
			if err != nil {
				t.Fatalf("NFO 坏掉不应让扫描失败: %v", err)
			}
			if result.UnreadableNFO != 1 {
				t.Fatalf("应报 1 个 unreadable_nfo，实际 %+v", result)
			}
			got, err := s.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "原始标题" || got.Plot != "原始简介" {
				t.Errorf("元数据被清空: title=%q plot=%q", got.Title, got.Plot)
			}
			if got.Status != "success" {
				t.Errorf("可见状态被降级: %q", got.Status)
			}
			if !strings.HasPrefix(got.LastScrapeError, store.ScanNFONotePrefix) {
				t.Errorf("原因未写入 last_scrape_error: %q", got.LastScrapeError)
			}
			if !visibleIDs(t, s)[id] {
				t.Error("保留的行必须仍然可见")
			}

			// 修好 NFO：下一次扫描应刷新元数据并清掉备注。
			write(nfoPath, "<movie><title>修好的标题</title></movie>")
			result, err = mode.scan()
			if err != nil {
				t.Fatal(err)
			}
			if result.UnreadableNFO != 0 {
				t.Errorf("修复后不应再计 unreadable_nfo: %+v", result)
			}
			fixed, err := s.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			if fixed.Title != "修好的标题" {
				t.Errorf("修复后应刷新标题，实际 %q", fixed.Title)
			}
			if fixed.LastScrapeError != "" {
				t.Errorf("修复后应清掉扫描备注，实际 %q", fixed.LastScrapeError)
			}

			// 复原初始状态，让另一种模式从同样的基线出发。
			write(nfoPath, "<movie><title>原始标题</title><plot>原始简介</plot></movie>")
			if _, err := mode.scan(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// 从没索引过的源遇到坏 NFO：仍要入库为 pending（用户能看到并修），并写原因。
func TestScanIndexesNewSourceWithUnreadableNFOAsPending(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "NEW-001.strm")
	if err := os.WriteFile(source, []byte("https://media.test/new.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "NEW-001.nfo"), []byte("<movie><title>坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Scan(s, lib)
	if err != nil {
		t.Fatal(err)
	}
	if result.UnreadableNFO != 1 {
		t.Fatalf("应报 1 个 unreadable_nfo，实际 %+v", result)
	}
	movie, err := s.Movie(mustID(t, s, source))
	if err != nil {
		t.Fatal(err)
	}
	if movie.Status != "pending" {
		t.Errorf("新源应入库为 pending，实际 %q", movie.Status)
	}
	if !strings.HasPrefix(movie.LastScrapeError, store.ScanNFONotePrefix) {
		t.Errorf("原因未写入 last_scrape_error: %q", movie.LastScrapeError)
	}
}

func mustID(t *testing.T, s *store.Store, path string) int64 {
	t.Helper()
	id, err := s.MovieIDByPath(path)
	if err != nil || id == 0 {
		t.Fatalf("取影片 id 失败: %d %v", id, err)
	}
	return id
}

func visibleIDs(t *testing.T, s *store.Store) map[int64]bool {
	t.Helper()
	ids := map[int64]bool{}
	for _, movie := range visible(t, s) {
		ids[movie.ID] = true
	}
	return ids
}
