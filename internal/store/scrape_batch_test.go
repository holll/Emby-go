package store

import "testing"

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
