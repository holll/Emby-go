package store

import (
	"fmt"
	"reflect"
	"testing"
)

// DB-10：刮削候选按 id 游标分批，游标推进不重不漏，筛选条件与全量查询一致。
func TestMoviesForScrapeBatchCursor(t *testing.T) {
	s, libID := newFeatureStore(t)
	for i := 1; i <= 5; i++ {
		upsertFixtureMovie(t, s, libID, Movie{
			LibraryID: libID, SourcePath: "/tmp/av/f" + string(rune('0'+i)) + ".strm",
			Status: "success", Title: "影片" + string(rune('0'+i)),
		})
	}
	var got []int64
	after := int64(0)
	for {
		batch, err := s.MoviesForScrapeBatch(libID, false, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		if len(batch) > 2 {
			t.Fatalf("批大小超限: %d", len(batch))
		}
		for _, m := range batch {
			got = append(got, m.ID)
		}
		after = batch[len(batch)-1].ID
	}
	if len(got) != 5 {
		t.Fatalf("遍历到 %d 条，期望 5: %v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("id 未严格递增: %v", got)
		}
	}
}

func TestMoviesForScrapeBatchOnlyMissingAndLibrary(t *testing.T) {
	s, libID := newFeatureStore(t)
	otherLib, err := s.AddLibrary("其他", "/tmp/other")
	if err != nil {
		t.Fatal(err)
	}
	// 有 NFO 且标题非空 → onlyMissing 不应返回；另一部缺 NFO → 应返回。
	withNFO := upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/a.strm", Status: "success", Title: "有 NFO", NFOPath: "/tmp/av/a.nfo",
	})
	missing := upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/b.strm", Status: "pending",
	})
	foreign := upsertFixtureMovie(t, s, otherLib.ID, Movie{
		LibraryID: otherLib.ID, SourcePath: "/tmp/other/c.strm", Status: "pending",
	})
	_ = withNFO

	batch, err := s.MoviesForScrapeBatch(libID, true, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].ID != missing {
		t.Fatalf("库内缺 NFO 候选 = %+v，期望仅 %d（foreign=%d）", batch, missing, foreign)
	}
}

func TestScrapeQueriesConsistentAtBoundaries(t *testing.T) {
	s, libID := newFeatureStore(t)
	other, err := s.AddLibrary("其他", "/tmp/other")
	if err != nil {
		t.Fatal(err)
	}
	// 故意不按 id 插入，包含非正 id，防止全量被错误替换为 Batch(afterID=0)。
	fixtures := []struct {
		id, libraryID int64
		title, nfo    string
		status        string
	}{
		{22, other.ID, "完整", "c.nfo", "manual"},
		{-5, libID, "负 id", "", "pending"},
		{11, libID, " ", " ", "success"}, // 空白不等于空串。
		{0, other.ID, "", "zero.nfo", "success"},
		{31, other.ID, "", "", "pending"},
		{7, libID, "", "b.nfo", "manual"},
		{3, libID, "完整", "a.nfo", "success"},
		{16, other.ID, "缺 NFO", "", "success"},
	}
	for i, f := range fixtures {
		id := upsertFixtureMovie(t, s, f.libraryID, Movie{
			LibraryID: f.libraryID, SourcePath: fmt.Sprintf("/tmp/scrape-%d.strm", i),
			Title: f.title, NFOPath: f.nfo, Status: f.status,
		})
		if _, err := s.db.Exec("UPDATE movies SET id=? WHERE id=?", f.id, id); err != nil {
			t.Fatal(err)
		}
	}
	// 明确列出排序后的 id，期望值不依赖被测 SQL。
	orderedIDs := []int64{-5, 0, 3, 7, 11, 16, 22, 31}
	for _, libraryID := range []int64{-3, 0, libID, other.ID, other.ID + 100} {
		for _, onlyMissing := range []bool{false, true} {
			t.Run(fmt.Sprintf("library=%d/missing=%t", libraryID, onlyMissing), func(t *testing.T) {
				var want []int64
				for _, id := range orderedIDs {
					for _, f := range fixtures {
						if f.id == id && (libraryID <= 0 || f.libraryID == libraryID) &&
							(!onlyMissing || f.nfo == "" || f.title == "") {
							want = append(want, id)
						}
					}
				}
				count, err := s.CountMoviesForScrape(libraryID, onlyMissing)
				if err != nil || count != len(want) {
					t.Fatalf("CountMoviesForScrape = %d, %v; want %d", count, err, len(want))
				}
				for _, limit := range []int{-2, 0, 1, 3, 50} {
					full, err := s.MoviesForScrape(libraryID, onlyMissing, limit)
					if err != nil {
						t.Fatal(err)
					}
					fullWant := want
					if limit > 0 && len(fullWant) > limit {
						fullWant = fullWant[:limit]
					}
					assertScrapeMovieIDs(t, full, fullWant, fmt.Sprintf("full limit=%d", limit))
					for _, after := range []int64{-6, -5, 0, 7, 31, 100} {
						var batchWant []int64
						for _, id := range want {
							if id > after {
								batchWant = append(batchWant, id)
							}
						}
						if limit > 0 && len(batchWant) > limit {
							batchWant = batchWant[:limit]
						}
						batch, err := s.MoviesForScrapeBatch(libraryID, onlyMissing, after, limit)
						if err != nil {
							t.Fatal(err)
						}
						assertScrapeMovieIDs(t, batch, batchWant, fmt.Sprintf("batch after=%d limit=%d", after, limit))
					}
				}
				// 实际翻页后的全集也应等于全量和计数，不能重复或漏掉非正 id。
				var paged []Movie
				for after := int64(-6); ; {
					batch, err := s.MoviesForScrapeBatch(libraryID, onlyMissing, after, 2)
					if err != nil {
						t.Fatal(err)
					}
					if len(batch) == 0 {
						break
					}
					if batch[0].ID <= after || len(paged)+len(batch) > len(want) {
						t.Fatalf("cursor failed to advance: after=%d, batch=%+v", after, batch)
					}
					paged = append(paged, batch...)
					after = batch[len(batch)-1].ID
				}
				assertScrapeMovieIDs(t, paged, want, "paged")
			})
		}
	}
}

func assertScrapeMovieIDs(t *testing.T, movies []Movie, want []int64, label string) {
	t.Helper()
	var got []int64
	for _, m := range movies {
		got = append(got, m.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s IDs = %v; want %v", label, got, want)
	}
	if len(want) == 0 && movies != nil {
		t.Fatalf("%s: empty result should remain nil, got %#v", label, movies)
	}
}

func TestScrapeQueriesNullMetadata(t *testing.T) {
	for _, column := range []string{"nfo_path", "title"} {
		t.Run(column, func(t *testing.T) {
			s, libID := newFeatureStore(t)
			id := upsertFixtureMovie(t, s, libID, Movie{
				LibraryID: libID, SourcePath: "/tmp/null.strm", Status: "success",
				Title: "有标题", NFOPath: "/tmp/null.nfo",
			})
			if _, err := s.db.Exec("UPDATE movies SET "+column+"=NULL WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
			for _, libraryID := range []int64{-1, 0, libID} {
				count, err := s.CountMoviesForScrape(libraryID, true)
				if err != nil || count != 1 {
					t.Fatalf("NULL %s count = %d, %v; want 1", column, count, err)
				}
				// 既有 movieScan 不能将 SQL NULL 扫入 string；本次仅共用筛选，保留该错误契约。
				if movies, err := s.MoviesForScrape(libraryID, true, 0); err == nil || movies != nil {
					t.Fatalf("NULL %s full = %+v, %v; want nil and scan error", column, movies, err)
				}
				if movies, err := s.MoviesForScrapeBatch(libraryID, true, 0, 0); err == nil || movies != nil {
					t.Fatalf("NULL %s batch = %+v, %v; want nil and scan error", column, movies, err)
				}
				movies, err := s.MoviesForScrapeBatch(libraryID, true, id, 0)
				if err != nil || movies != nil {
					t.Fatalf("cursor should exclude NULL row: %+v, %v", movies, err)
				}
			}
		})
	}
}
