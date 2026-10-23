package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStackedNFOPathPreference(t *testing.T) {
	for _, tc := range []struct {
		name, shared, legacy, wantFile, wantTitle string
		pending                                   bool
	}{
		{name: "shared", shared: "<movie><title>Shared</title></movie>", wantFile: "GOPJ-559.nfo", wantTitle: "Shared"},
		{name: "legacy", legacy: "<movie><title>Legacy</title></movie>", wantFile: "GOPJ-559-CD1.nfo", wantTitle: "Legacy"},
		{name: "preserve legacy precedence", shared: "<movie><title>Shared</title></movie>", legacy: "<movie><title>Legacy</title></movie>", wantFile: "GOPJ-559-CD1.nfo", wantTitle: "Legacy"},
		{name: "invalid shared with legacy", shared: "<movie><title>Broken", legacy: "<movie><title>Legacy</title></movie>", wantFile: "GOPJ-559-CD1.nfo", wantTitle: "Legacy"},
		{name: "invalid legacy falls back to shared", shared: "<movie><title>Shared</title></movie>", legacy: "<movie><title>Broken", wantFile: "GOPJ-559.nfo", wantTitle: "Shared"},
		{name: "invalid shared", shared: "<movie><title>Broken", pending: true},
		{name: "missing", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("GOPJ-559-CD1.strm", "https://media.test/1.mp4\n")
			write("GOPJ-559-CD2.strm", "https://media.test/2.mp4\n")
			if tc.shared != "" {
				write("GOPJ-559.nfo", tc.shared)
			}
			if tc.legacy != "" {
				write("GOPJ-559-CD1.nfo", tc.legacy)
			}
			s := newStore(t, root)
			lib, err := s.AddLibrary("test", root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Scan(s, lib); err != nil {
				t.Fatal(err)
			}
			check := func() {
				t.Helper()
				id, err := s.MovieIDByPath(filepath.Join(root, "GOPJ-559-CD1.strm"))
				if err != nil {
					t.Fatal(err)
				}
				m, err := s.Movie(id)
				if err != nil {
					t.Fatal(err)
				}
				if len(m.AdditionalParts) != 1 {
					t.Fatalf("parts=%v", m.AdditionalParts)
				}
				if tc.pending {
					if m.Status != "pending" {
						t.Fatalf("expected pending, got %+v", m)
					}
					return
				}
				if m.NFOPath != filepath.Join(root, tc.wantFile) || m.Title != tc.wantTitle {
					t.Fatalf("path/title=%q/%q, want %q/%q", m.NFOPath, m.Title, tc.wantFile, tc.wantTitle)
				}
			}
			check()
			// Rescanning either segment must resolve the same primary and NFO.
			for _, part := range []string{"GOPJ-559-CD1.strm", "GOPJ-559-CD2.strm"} {
				if _, err := RescanOne(s, lib, filepath.Join(root, part)); err != nil {
					t.Fatal(err)
				}
				check()
			}
		})
	}
}
