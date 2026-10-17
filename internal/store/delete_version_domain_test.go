package store

import (
	"fmt"
	"testing"
	"time"
)

func TestDeleteIndexedVersionDomainsIncludeRealZeroLibrary(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprintf("actual_zero_%t", zero), func(t *testing.T) {
			s, libraryID := newFeatureStore(t)
			s.waitFeaturesReady()
			if zero {
				if _, err := s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/fixture/zero')"); err != nil {
					t.Fatal(err)
				}
				libraryID = 0
			}
			// Seed a real lib:0 version independently of the selected library.
			if err := s.BumpVersion(0); err != nil {
				t.Fatal(err)
			}
			id, err := s.IndexMovie(Movie{LibraryID: libraryID, SourcePath: "/fixture/version-domain.strm", Title: "Delete", Status: "success"}, 0, time.Unix(1, 0), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteIndexedMovie(id); err != nil {
				t.Fatal(err)
			}
			wantZero := "1"
			if zero {
				wantZero = "3"
			}
			if got := storedVersion(t, s, "lib:0:version"); got != wantZero {
				t.Fatalf("lib0=%s want=%s (zero must not be a sentinel or double bumped)", got, wantZero)
			}
			wantActual := "2"
			if zero {
				wantActual = "3"
			}
			if got := storedVersion(t, s, fmt.Sprintf("lib:%d:version", libraryID)); got != wantActual {
				t.Fatalf("actual library version=%s want=%s", got, wantActual)
			}
			if got := storedVersion(t, s, "g:version"); got != "3" || s.Version("g:version") != got {
				t.Fatalf("global=%s mirror=%s", got, s.Version("g:version"))
			}
		})
	}
}
