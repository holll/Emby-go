package nfo

import (
	"path/filepath"
	"testing"
)

func TestSidecarPath(t *testing.T) {
	for _, tc := range []struct {
		source  string
		stacked bool
		want    string
	}{
		{"GOPJ-559-CD1.strm", true, "GOPJ-559.nfo"},
		{"GOPJ-559-CD2.strm", true, "GOPJ-559.nfo"},
		{"Movie.cd1.STRM", true, "Movie.nfo"},
		{"Movie_CD10.strm", true, "Movie.nfo"},
		{"Movie CD1.strm", true, "Movie.nfo"},
		{"GOPJ-559.strm", false, "GOPJ-559.nfo"},
		{"Movie-CD1.strm", false, "Movie-CD1.nfo"},
		{"Movie-CD2.strm", false, "Movie-CD2.nfo"},
		{"Movie-CD1-U.strm", true, "Movie-CD1-U.nfo"},
		{"Movie-CD0.strm", true, "Movie-CD0.nfo"},
		{"MovieCD1.strm", true, "MovieCD1.nfo"},
	} {
		t.Run(tc.source+"/"+tc.want, func(t *testing.T) {
			source := filepath.Join("media", "目录-CD1", tc.source)
			want := filepath.Join("media", "目录-CD1", tc.want)
			if got := SidecarPath(source, tc.stacked); got != want {
				t.Fatalf("SidecarPath(%q, %v) = %q, want %q", source, tc.stacked, got, want)
			}
		})
	}
}
