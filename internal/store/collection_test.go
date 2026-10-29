package store

import (
	"slices"
	"testing"
)

func TestCollectionStatsVisibleScopesAllMetadata(t *testing.T) {
	s, lib := newFeatureStore(t)
	other, err := s.AddLibrary("private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Movie{
		{LibraryID: lib, SourcePath: "/tmp/visible/a.strm", Status: "success", Title: "A", Collection: "Shared", PosterPath: "visible.jpg", Genres: []string{"Public"}},
		{LibraryID: other.ID, SourcePath: "/tmp/private/b.strm", Status: "success", Title: "B", Collection: "Shared", PosterPath: "private.jpg", Genres: []string{"Secret"}},
		{LibraryID: other.ID, SourcePath: "/tmp/private/c.strm", Status: "success", Title: "C", Collection: "Private"},
	} {
		upsertFixtureMovie(t, s, m.LibraryID, m)
	}
	stats, err := s.CollectionStatsVisible(1, 0, []int64{lib})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats["Shared"].Count != 1 || stats["Shared"].Poster != "visible.jpg" || !slices.Equal(stats["Shared"].Genres, []string{"Public"}) {
		t.Fatalf("scoped stats leaked metadata: %+v", stats)
	}
	stats, err = s.CollectionStatsVisible(2, 0, []int64{lib})
	if err != nil || len(stats) != 0 {
		t.Fatalf("threshold must apply after grants: %+v %v", stats, err)
	}
	stats, err = s.CollectionStatsVisible(1, 0, []int64{})
	if err != nil || len(stats) != 0 {
		t.Fatalf("no grants: %+v %v", stats, err)
	}
	stats, err = s.CollectionStatsVisible(1, other.ID, []int64{lib})
	if err != nil || len(stats) != 0 {
		t.Fatalf("library must intersect grants: %+v %v", stats, err)
	}
	stats, err = s.CollectionStatsVisible(2, 0, nil)
	if err != nil || stats["Shared"].Count != 2 {
		t.Fatalf("unrestricted stats: %+v %v", stats, err)
	}
}

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
