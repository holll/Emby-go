package store

import "testing"

// TestCollectionsMinMovies 合集列表按最低影片数过滤：
// 只有一部影片的 <set> 不该被当成合集（除非阈值放到 1）。
func TestCollectionsMinMovies(t *testing.T) {
	s, libID := newFeatureStore(t)
	upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/s1.strm", Status: "success", Title: "S1", Collection: "系列A",
	})
	upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/s2.strm", Status: "success", Title: "S2", Collection: "系列A",
	})
	upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/s3.strm", Status: "success", Title: "S3", Collection: "孤本",
	})
	// 只有一部可见影片的合集，阈值 2 时应被过滤掉。
	upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/s4.strm", Status: "pending", Title: "S4", Collection: "系列B",
	})

	cases := []struct {
		min  int
		want []string
	}{
		{2, []string{"系列A"}},
		{1, []string{"孤本", "系列A"}}, // SQLite 按 UTF-8 字节序排，孤(U+5B64) < 系(U+7CFB)
		{3, nil},
	}
	for _, tc := range cases {
		got, err := s.Collections(tc.min)
		if err != nil {
			t.Fatalf("Collections(%d): %v", tc.min, err)
		}
		if len(got) != len(tc.want) {
			t.Errorf("Collections(%d) = %v，期望 %v", tc.min, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("Collections(%d) = %v，期望 %v", tc.min, got, tc.want)
				break
			}
		}
		stats, err := s.CollectionStats(tc.min)
		if err != nil {
			t.Fatalf("CollectionStats(%d): %v", tc.min, err)
		}
		if len(stats) != len(tc.want) {
			t.Errorf("CollectionStats(%d) 返回 %d 个合集，期望 %d 个: %+v", tc.min, len(stats), len(tc.want), stats)
		}
	}
	// 统计里的影片数也要与实际一致（系列A 两部；pending 的那部不计入）。
	stats, err := s.CollectionStats(2)
	if err != nil {
		t.Fatal(err)
	}
	if stats["系列A"].Count != 2 {
		t.Errorf("系列A Count = %d，期望 2", stats["系列A"].Count)
	}
}
