package store

import (
	"path/filepath"
	"testing"
)

func collectionCoverFixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// 新库迁移后表即存在：AppliedCovers 返回空 map 而不是错误。
func TestCollectionCoverEmptyAfterMigration(t *testing.T) {
	s, _ := collectionCoverFixture(t)
	covers, err := s.AppliedCovers()
	if err != nil {
		t.Fatalf("AppliedCovers: %v", err)
	}
	if len(covers) != 0 {
		t.Fatalf("新库应无封面记录，得到 %+v", covers)
	}
}

// SetAppliedCover 写入后 AppliedCover 读回全部字段（含 width/height）。
func TestCollectionCoverSetAndRead(t *testing.T) {
	s, _ := collectionCoverFixture(t)
	want := CollectionCover{
		Collection: "系列A",
		Path:       "/covers/系列A.jpg",
		SourceHash: "hash-v1",
		Algorithm:  "strip-v1",
		Width:      1200,
		Height:     1800,
		AppliedAt:  "2026-11-12T08:00:00Z",
	}
	if err := s.SetAppliedCover(want); err != nil {
		t.Fatalf("SetAppliedCover: %v", err)
	}
	got, ok, err := s.AppliedCover(want.Collection)
	if err != nil {
		t.Fatalf("AppliedCover: %v", err)
	}
	if !ok {
		t.Fatal("AppliedCover 未命中已写入的记录")
	}
	if got != want {
		t.Fatalf("读回 %+v，期望 %+v", got, want)
	}
}

// 同一合集再写一次是覆盖（主键 upsert），不会变成两行。
func TestCollectionCoverOverwrites(t *testing.T) {
	s, _ := collectionCoverFixture(t)
	first := CollectionCover{
		Collection: "系列A", Path: "/covers/a-v1.jpg", SourceHash: "hash-v1",
		Algorithm: "strip-v1", Width: 1200, Height: 1800, AppliedAt: "2026-11-12T08:00:00Z",
	}
	second := CollectionCover{
		Collection: "系列A", Path: "/covers/a-v2.jpg", SourceHash: "hash-v2",
		Algorithm: "strip-v2", Width: 900, Height: 600, AppliedAt: "2026-11-12T09:00:00Z",
	}
	if err := s.SetAppliedCover(first); err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	if err := s.SetAppliedCover(second); err != nil {
		t.Fatalf("覆盖写入: %v", err)
	}
	covers, err := s.AppliedCovers()
	if err != nil {
		t.Fatalf("AppliedCovers: %v", err)
	}
	if len(covers) != 1 {
		t.Fatalf("覆盖后应有 1 条，得到 %d 条: %+v", len(covers), covers)
	}
	if got := covers[second.Collection]; got != second {
		t.Fatalf("覆盖后读到 %+v，期望 %+v", got, second)
	}
}

// Clear 之后读不到；对不存在的合集再 Clear 也必须返回 nil。
func TestCollectionCoverClear(t *testing.T) {
	s, _ := collectionCoverFixture(t)
	cover := CollectionCover{
		Collection: "系列A", Path: "/covers/a.jpg", SourceHash: "hash-v1",
		Algorithm: "strip-v1", Width: 1200, Height: 1800, AppliedAt: "2026-11-12T08:00:00Z",
	}
	if err := s.SetAppliedCover(cover); err != nil {
		t.Fatalf("SetAppliedCover: %v", err)
	}
	if err := s.ClearAppliedCover(cover.Collection); err != nil {
		t.Fatalf("ClearAppliedCover: %v", err)
	}
	if _, ok, err := s.AppliedCover(cover.Collection); err != nil || ok {
		t.Fatalf("清除后仍能读到: ok=%v err=%v", ok, err)
	}
	if err := s.ClearAppliedCover(cover.Collection); err != nil {
		t.Fatalf("重复清除应返回 nil，得到 %v", err)
	}
}

// 重新打开同一库文件：迁移幂等，记录仍在。
func TestCollectionCoverSurvivesReopen(t *testing.T) {
	s, path := collectionCoverFixture(t)
	cover := CollectionCover{
		Collection: "系列A", Path: "/covers/a.jpg", SourceHash: "hash-v1",
		Algorithm: "strip-v1", Width: 1200, Height: 1800, AppliedAt: "2026-11-12T08:00:00Z",
	}
	if err := s.SetAppliedCover(cover); err != nil {
		t.Fatalf("SetAppliedCover: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开: %v", err)
	}
	defer reopened.Close()
	got, ok, err := reopened.AppliedCover(cover.Collection)
	if err != nil || !ok {
		t.Fatalf("重开后读不到记录: ok=%v err=%v", ok, err)
	}
	if got != cover {
		t.Fatalf("重开后读到 %+v，期望 %+v", got, cover)
	}
}

// 迁移 23 必须已登记且 checksum 与 migration23Checksum() 一致。
func TestCollectionCoverMigrationRegistered(t *testing.T) {
	s, _ := collectionCoverFixture(t)
	var checksum string
	if err := s.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=?", collectionCoverMigrationVersion).Scan(&checksum); err != nil {
		t.Fatalf("schema_migrations 无 version=%d: %v", collectionCoverMigrationVersion, err)
	}
	if checksum != migration23Checksum() {
		t.Fatalf("checksum=%s 期望 %s", checksum, migration23Checksum())
	}
}
