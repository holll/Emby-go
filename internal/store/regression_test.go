package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCacheNamespacePersistsAndIsolatesDatabases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CacheNamespace()
	if err != nil || len(a) != 64 {
		t.Fatalf("实例身份: %q, %v", a, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := s.CacheNamespace()
	if err != nil || a != b {
		t.Fatalf("重启后身份漂移: %q %q, %v", a, b, err)
	}
	other, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	c, err := other.CacheNamespace()
	if err != nil || c == a {
		t.Fatalf("不同数据库不能共用身份: %q %q, %v", a, c, err)
	}
}

func TestAddLibraryConflictReturnsOwnID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.waitFeaturesReady()
	s.db.SetMaxOpenConns(1)
	a, err := s.AddLibrary("A", "/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLibrary("B", "/b"); err != nil {
		t.Fatal(err)
	}
	updated, err := s.AddLibrary("A2", "/a")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != a.ID {
		t.Fatalf("更新返回ID=%d, 目标ID=%d", updated.ID, a.ID)
	}
	actual, err := s.Library(updated.ID)
	if err != nil || actual.Name != "A2" {
		t.Fatalf("更新结果错误: %+v, %v", actual, err)
	}
}

func TestPendingStatusFiltersCoverAllHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lib, err := s.AddLibrary("L", "/library")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new", "succeeded", "failed"} {
		id, err := s.UpsertMovie(Movie{LibraryID: lib.ID, SourcePath: "/library/" + name + ".strm", Status: "pending", Title: name}, 0, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if name == "succeeded" {
			if err := s.SetScrapeResult(id, ""); err != nil {
				t.Fatal(err)
			}
		}
		if name == "failed" {
			if err := s.SetScrapeResult(id, "failed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := map[int64]bool{}
	for _, status := range []string{StatusUnscraped, StatusNeedsAttention} {
		items, _, err := s.SearchAdmin(AdminQuery{Status: status, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("两个筛选不互斥: %d", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pending分类遗漏，覆盖%d/3", len(seen))
	}
}
