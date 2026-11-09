package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestMovieMultiKeySortCompatibility(t *testing.T) {
	s, lib := newFeatureStore(t)
	for i, m := range []Movie{
		{Title: "Z title", SortName: "A sort"},
		{Title: "A title", SortName: "Z sort"},
		{Title: "M title"},
	} {
		m.LibraryID, m.SourcePath, m.Status = lib, fmt.Sprintf("/tmp/sort-%d.strm", i), "success"
		id := upsertFixtureMovie(t, s, lib, m)
		created := "2026-01-02T00:00:00Z"
		if i == 2 {
			created = "2026-01-01T00:00:00Z"
		}
		if _, err := s.db.Exec("UPDATE movies SET created_at=? WHERE id=?", created, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		key           string
		desc          bool
		limit, offset int
		want          []string
	}{
		{"SortName", false, 10, 0, []string{"Z title", "M title", "A title"}},
		{"DateCreated,SortName", false, 10, 0, []string{"M title", "Z title", "A title"}},
		{"DateLastContentAdded,SortName", true, 10, 0, []string{"A title", "Z title", "M title"}},
		{" DateCreated , SortName ", false, 1, 1, []string{"Z title"}},
		{"title; DROP TABLE movies;--", false, 10, 0, []string{"A title", "M title", "Z title"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			movies, total, err := s.SearchScopedVisible(1, lib, nil, "", "", "", "", "", "", "", false, false, tc.key, tc.desc, tc.limit, tc.offset, "")
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(movies))
			for _, m := range movies {
				names = append(names, m.Title)
			}
			if total != 3 || !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("got %v total=%d want %v", names, total, tc.want)
			}
		})
	}
}
