package store

import (
	"fmt"
	"slices"
	"testing"
)

func TestEmbyMovieListFiltersBeforeCountAndPagination(t *testing.T) {
	s, lib := newFeatureStore(t)
	private, err := s.AddLibrary("private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Movie{
		{Title: "none"},
		{Title: "poster", PosterPath: "/not-on-disk/poster.jpg"},
		{Title: "landscape", LandscapePath: "/not-on-disk/landscape.jpg"},
		{Title: "extra", ExtraBackdrops: []string{"/not-on-disk/extra.jpg"}},
		{Title: "backdrop", BackdropPath: "/not-on-disk/backdrop.jpg"},
		{Title: "metatube", ProviderID: "12345"},
		{Title: "empty-extra", ExtraBackdrops: []string{""}},
		{Title: "malformed-extra"},
		{Title: "private", LibraryID: private.ID, PosterPath: "/not-on-disk/private.jpg"},
		{Title: "pending", Status: "pending", PosterPath: "/not-on-disk/pending.jpg"},
	} {
		if m.LibraryID == 0 {
			m.LibraryID = lib
		}
		if m.Status == "" {
			m.Status = "success"
		}
		m.SourcePath = fmt.Sprintf("/tmp/filters/%s.strm", m.Title)
		m.Collection = "Shared"
		id := upsertFixtureMovie(t, s, m.LibraryID, m)
		if m.Title == "malformed-extra" {
			if _, err := s.db.Exec("UPDATE movies SET extra_backdrops='not-json' WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		library   int64
		visible   []int64
		filter    MovieListFilter
		limit     int
		offset    int
		want      []string
		wantTotal int
	}{
		{name: "primary indexed path without disk file", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Primary"}}, want: []string{"poster"}, wantTotal: 1},
		{name: "thumb falls back to poster", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Thumb"}}, want: []string{"landscape", "poster"}, wantTotal: 2},
		{name: "backdrop includes extras only", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Backdrop"}}, want: []string{"backdrop", "extra"}, wantTotal: 2},
		{name: "image OR", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Primary", "Backdrop"}}, want: []string{"backdrop", "extra", "poster"}, wantTotal: 3},
		{name: "image OR paginated", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Primary", "Backdrop"}}, limit: 1, offset: 1, want: []string{"extra"}, wantTotal: 3},
		{name: "page beyond end retains count", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Primary", "Backdrop"}}, limit: 1, offset: 10, wantTotal: 3},
		{name: "unknown image", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"unknown"}}},
		{name: "banner is not landscape", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"Banner"}}},
		{name: "unknown does not cancel OR", visible: []int64{lib}, filter: MovieListFilter{ImageTypes: []string{"unknown", "PRIMARY"}}, want: []string{"poster"}, wantTotal: 1},
		{name: "no visible libraries", visible: []int64{}, filter: MovieListFilter{ImageTypes: []string{"Primary"}}},
		{name: "multiple visible libraries", visible: []int64{lib, private.ID}, filter: MovieListFilter{ImageTypes: []string{"Primary"}}, want: []string{"poster", "private"}, wantTotal: 2},
		{name: "unrestricted libraries", filter: MovieListFilter{ImageTypes: []string{"Primary"}}, want: []string{"poster", "private"}, wantTotal: 2},
		{name: "explicit library", library: private.ID, filter: MovieListFilter{ImageTypes: []string{"Primary"}}, want: []string{"private"}, wantTotal: 1},
		{name: "tmdb true cannot use metatube", visible: []int64{lib}, filter: MovieListFilter{HasTmdbID: &yes}},
		{name: "tmdb false does not restrict", visible: []int64{lib}, filter: MovieListFilter{HasTmdbID: &no}, want: []string{"backdrop", "empty-extra", "extra", "landscape", "malformed-extra", "metatube", "none", "poster"}, wantTotal: 8},
		{name: "tmdb false combined with image", visible: []int64{lib}, filter: MovieListFilter{HasTmdbID: &no, ImageTypes: []string{"Primary"}}, want: []string{"poster"}, wantTotal: 1},
		{name: "tmdb true combined with image", visible: []int64{lib}, filter: MovieListFilter{HasTmdbID: &yes, ImageTypes: []string{"Primary"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := tc.limit
			if limit == 0 {
				limit = 100
			}
			movies, total, err := s.SearchScopedVisible(1, tc.library, tc.visible, "Shared", "", "", "", "", "", "", false, false, "Name", false, limit, tc.offset, "", tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range movies {
				got = append(got, m.Title)
			}
			if total != tc.wantTotal || !slices.Equal(got, tc.want) {
				t.Fatalf("movies=%v total=%d, want %v total=%d", got, total, tc.want, tc.wantTotal)
			}
		})
	}
}

func TestEmbyPremiereDateSortAlias(t *testing.T) {
	s, lib := newFeatureStore(t)
	for i, m := range []Movie{
		{Title: "A", Premiere: "2024-01-01"},
		{Title: "Z", Premiere: "2020-01-01"},
		{Title: "M", Premiere: "2024-01-01"},
	} {
		m.LibraryID, m.SourcePath, m.Status = lib, fmt.Sprintf("/tmp/premiere/%d.strm", i), "success"
		upsertFixtureMovie(t, s, lib, m)
	}
	for _, tc := range []struct {
		key    string
		desc   bool
		limit  int
		offset int
		want   []string
	}{
		{key: "PremiereDate", limit: 10, want: []string{"Z", "A", "M"}},
		{key: "pReMiErEdAtE", desc: true, limit: 10, want: []string{"A", "M", "Z"}},
		{key: "PremiereDate,Name", limit: 1, offset: 1, want: []string{"A"}},
		{key: "Date", limit: 10, want: []string{"Z", "A", "M"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			movies, total, err := s.SearchScopedVisible(1, 0, []int64{lib}, "", "", "", "", "", "", "", false, false, tc.key, tc.desc, tc.limit, tc.offset, "")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range movies {
				got = append(got, m.Title)
			}
			if total != 3 || !slices.Equal(got, tc.want) {
				t.Fatalf("movies=%v total=%d, want %v total=3", got, total, tc.want)
			}
		})
	}
}

func TestEmbyCollectionLatestMovieIDUsesScopedNewestMember(t *testing.T) {
	s, lib := newFeatureStore(t)
	private, err := s.AddLibrary("private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	posterID := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/latest/poster.strm", Status: "success", Collection: "Shared", PosterPath: "poster.jpg"})
	newestID := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/latest/newest.strm", Status: "manual", Collection: "Shared"})
	privateID := upsertFixtureMovie(t, s, private.ID, Movie{LibraryID: private.ID, SourcePath: "/tmp/latest/private.strm", Status: "success", Collection: "Shared", PosterPath: "private.jpg"})
	upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/latest/pending.strm", Status: "pending", Collection: "Shared", PosterPath: "pending.jpg"})
	for _, tc := range []struct {
		name      string
		library   int64
		visible   []int64
		min       int
		wantID    int64
		wantCount int
		poster    string
	}{
		{name: "visible latest need not have poster", visible: []int64{lib}, min: 1, wantID: newestID, wantCount: 2, poster: "poster.jpg"},
		{name: "all libraries", min: 1, wantID: privateID, wantCount: 3, poster: "private.jpg"},
		{name: "multiple granted libraries", visible: []int64{lib, private.ID}, min: 1, wantID: privateID, wantCount: 3, poster: "private.jpg"},
		{name: "explicit scoped library", library: lib, visible: []int64{lib, private.ID}, min: 1, wantID: newestID, wantCount: 2, poster: "poster.jpg"},
		{name: "no access", visible: []int64{}, min: 1},
		{name: "threshold after scope", visible: []int64{lib}, min: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats, err := s.CollectionStatsVisible(tc.min, tc.library, tc.visible)
			if err != nil {
				t.Fatal(err)
			}
			stat := stats["Shared"]
			if stat.LatestMovieID != tc.wantID || stat.Count != tc.wantCount || stat.Poster != tc.poster {
				t.Fatalf("stat=%+v, want latest=%d count=%d poster=%q (older poster ID=%d)", stat, tc.wantID, tc.wantCount, tc.poster, posterID)
			}
			if tc.wantCount == 0 && len(stats) != 0 {
				t.Fatalf("unexpected collections: %+v", stats)
			}
		})
	}
}
