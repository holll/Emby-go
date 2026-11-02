package store

import "testing"

func TestCleanDisplayName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FC2-PPV-1050737-CD1", "FC2-PPV-1050737"},
		{"Movie_cd2", "Movie"}, {"Movie.cd10", "Movie"},
		{" Movie CD123 ", "Movie"}, {"Movie - CD2", "Movie"},
		{"Movie-CD0", "Movie-CD0"}, {"MovieCD1", "MovieCD1"},
		{"Movie-CD1-U", "Movie-CD1-U"}, {"CD1 的故事", "CD1 的故事"},
		{"Movie-CD1extra", "Movie-CD1extra"}, {"ABF-018", "ABF-018"},
		{"", ""},
	} {
		if got := CleanDisplayName(tc.in); got != tc.want {
			t.Errorf("CleanDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMovieDisplayNamesPreserveSource(t *testing.T) {
	for _, tc := range []struct {
		name       string
		movie      Movie
		want, sort string
	}{
		{"title", Movie{Title: "Title-CD1", Number: "ABF-018", SortName: "Sort-CD2"}, "Title", "Sort"},
		{"number", Movie{Title: " ", Number: "ABF-018-CD1"}, "ABF-018", "ABF-018"},
		{"single part", Movie{SourcePath: "/media/FC2-CD1/FC2-PPV-1050737-CD1.strm"}, "FC2-PPV-1050737", "FC2-PPV-1050737"},
		{"windows", Movie{SourcePath: `C:\media\Movie-CD10.strm`}, "Movie", "Movie"},
		{"empty", Movie{}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.movie
			if got := tc.movie.DisplayTitle(); got != tc.want {
				t.Errorf("DisplayTitle() = %q, want %q", got, tc.want)
			}
			if got := tc.movie.DisplaySortName(); got != tc.sort {
				t.Errorf("DisplaySortName() = %q, want %q", got, tc.sort)
			}
			if tc.movie.SourcePath != before.SourcePath || tc.movie.Title != before.Title || tc.movie.Number != before.Number || tc.movie.SortName != before.SortName {
				t.Fatal("display cleanup modified stored fields")
			}
		})
	}
}
