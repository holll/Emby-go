package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanMetaTubePluginIdentity(t *testing.T) {
	for _, tc := range []struct{ name, tags, want string }{
		{"plugin legacy tag", `<metatubeid>FANZA:abc%2B123:0.5</metatubeid>`, "FANZA:abc%2B123:0.5"},
		{"uniqueid", `<uniqueid type="MetaTube">FANZA:abc123</uniqueid>`, "FANZA:abc123"},
		{"uniqueid precedence", `<metatubeid>Old:wrong</metatubeid><uniqueid type="metatube">New:right</uniqueid>`, "New:right"},
		{"local legacy", `<uniqueid type="metatube">metatube:FANZA:abc</uniqueid>`, "metatube:FANZA:abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range map[string]string{"movie.strm": "https://media.test/movie.mp4\n", "movie.nfo": "<movie><title>Movie</title>" + tc.tags + "</movie>"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			s := newStore(t, root)
			lib, err := s.AddLibrary("test", root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Scan(s, lib); err != nil {
				t.Fatal(err)
			}
			movies := visible(t, s)
			if len(movies) != 1 || movies[0].ProviderID != tc.want {
				t.Fatalf("identity not preserved: %+v", movies)
			}
		})
	}
}
