package scraper

import (
	"context"
	"fmt"
	"testing"
	"time"

	"emby-go/internal/store"
)

// DB-10：Run 不再整库装载候选，250 部（>200 批大小）也要全部处理到。
func TestRunStreamsCandidatesAcrossBatches(t *testing.T) {
	backend := fakeBackend(t)
	sc := newTestScraper(t, backend.URL, DefaultConfig())
	lib, err := sc.store.AddLibrary("AV", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if _, err := sc.store.UpsertMovie(store.Movie{
			LibraryID: lib.ID, SourcePath: fmt.Sprintf("/tmp/av/x%03d.strm", i),
			Status: "pending",
		}, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	progress, err := sc.Run(context.Background(), RunOptions{LibraryID: lib.ID, OnlyMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Total != 250 || progress.Done != 250 {
		t.Fatalf("进度 = %+v，期望 total/done 均为 250", progress)
	}
	if progress.Done != progress.Success+progress.Skipped+progress.Failed {
		t.Fatalf("done 与分类合计不一致: %+v", progress)
	}
}

// Limit 跨批生效：只处理前 N 部，Total 也按 N 报告。
func TestRunStreamsHonorsLimit(t *testing.T) {
	backend := fakeBackend(t)
	sc := newTestScraper(t, backend.URL, DefaultConfig())
	lib, err := sc.store.AddLibrary("AV", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := sc.store.UpsertMovie(store.Movie{
			LibraryID: lib.ID, SourcePath: fmt.Sprintf("/tmp/av/y%d.strm", i),
			Status: "pending",
		}, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	progress, err := sc.Run(context.Background(), RunOptions{LibraryID: lib.ID, OnlyMissing: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Total != 2 || progress.Done != 2 {
		t.Fatalf("进度 = %+v，期望 total/done 均为 2", progress)
	}
}
