package store

import (
	"fmt"
	"slices"
	"testing"
)

func TestPlayedMovieListFilter(t *testing.T) {
	s, lib := newFeatureStore(t)
	owner := seedFixtureAdministrator(t, s, "owner", "owner-password")
	other := seedFixtureUser(t, s, "other", "other-password", false)
	private, err := s.AddLibrary("private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, name := range []string{"A played", "B unplayed", "C absent", "D other", "E private", "F pending"} {
		m := Movie{LibraryID: lib, SourcePath: fmt.Sprintf("/tmp/played/%s.strm", name), Title: name, Status: "success", Collection: "Shared"}
		if name == "E private" {
			m.LibraryID = private.ID
		}
		if name == "F pending" {
			m.Status = "pending"
		}
		ids[name] = upsertFixtureMovie(t, s, m.LibraryID, m)
	}
	for _, name := range []string{"A played", "E private"} {
		if _, err := s.SetIndexedPlayedDataForUser(owner.ID, ids[name], true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetIndexedPlayedDataForUser(owner.ID, ids["B unplayed"], false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIndexedPlayedDataForUser(other.ID, ids["D other"], true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIndexedFavoriteDataForUser(owner.ID, ids["A played"], true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		user, library    int64
		visible          []int64
		collection       string
		played, unplayed bool
		favorite         bool
		limit, offset    int
		want             []string
		total            int
	}{
		{name: "default unchanged", user: owner.ID, want: []string{"A played", "B unplayed", "C absent", "D other", "E private"}, total: 5},
		{name: "current user only", user: owner.ID, played: true, want: []string{"A played", "E private"}, total: 2},
		{name: "other user", user: other.ID, played: true, want: []string{"D other"}, total: 1},
		{name: "visible libraries", user: owner.ID, visible: []int64{lib}, played: true, want: []string{"A played"}, total: 1},
		{name: "no grants", user: owner.ID, visible: []int64{}, played: true},
		{name: "library", user: owner.ID, library: private.ID, played: true, want: []string{"E private"}, total: 1},
		{name: "collection", user: owner.ID, collection: "Shared", played: true, want: []string{"A played", "E private"}, total: 2},
		{name: "count before pagination", user: owner.ID, played: true, limit: 1, offset: 1, want: []string{"E private"}, total: 2},
		{name: "beyond page", user: owner.ID, played: true, limit: 1, offset: 2, total: 2},
		{name: "played and favorite", user: owner.ID, played: true, favorite: true, want: []string{"A played"}, total: 1},
		{name: "contradictory played", user: owner.ID, played: true, unplayed: true},
		{name: "unplayed unchanged", user: owner.ID, unplayed: true, want: []string{"B unplayed", "C absent", "D other"}, total: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := tc.limit
			if limit == 0 {
				limit = 100
			}
			movies, total, err := s.SearchScopedVisible(tc.user, tc.library, tc.visible, tc.collection, "", "", "", "", "", "", tc.unplayed, tc.favorite, "Name", false, limit, tc.offset, "", MovieListFilter{Played: tc.played})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, movie := range movies {
				got = append(got, movie.Title)
			}
			if total != tc.total || !slices.Equal(got, tc.want) {
				t.Fatalf("items=%v total=%d, want %v total=%d", got, total, tc.want, tc.total)
			}
		})
	}
}
