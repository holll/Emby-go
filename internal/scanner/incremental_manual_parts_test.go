package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIncrementalManualMissingNFORetainsCurrentPartsAndZeroWriteBaseline(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "Film-CD1.strm")
	part := filepath.Join(root, "Film-CD2.strm")
	sidecar := filepath.Join(root, "Film-CD1.nfo")
	scanWrite(t, primary, "https://media.test/1.mp4")
	scanWrite(t, sidecar, "<movie><title>Manual Film</title><genre>Drama</genre><actor><name>Actor</name></actor></movie>")
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	id, err := s.MovieIDByPath(primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIndexedManual(primary, sidecar); err != nil {
		t.Fatal(err)
	}
	before, err := s.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	actors, err := s.Actors(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlaybackForUser(1, id, 1234, 2, "2026-10-23T01:00:00Z", -1); err != nil {
		t.Fatal(err)
	}
	userdata, err := s.DataUser(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	db := scanDB(t, root)
	for _, scenario := range []string{"add-part", "remove-part"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "add-part" {
				scanWrite(t, part, "https://media.test/2.mp4")
			} else if err := os.Remove(part); err != nil {
				t.Fatal(err)
			}
			previousVersion := s.Version("g:version")
			result, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
			if err != nil || result.Updated != 1 || result.Success != 1 || s.Version("g:version") == previousVersion {
				t.Fatalf("structural update %+v %v version=%s/%s", result, err, previousVersion, s.Version("g:version"))
			}
			movie, err := s.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			if movie.Title != before.Title || movie.Status != "manual" || movie.CreatedAt != before.CreatedAt || movie.ID != before.ID {
				t.Fatalf("manual metadata/identity lost: %+v", movie)
			}
			wantParts := []string(nil)
			if scenario == "add-part" {
				wantParts = []string{part}
			}
			if !reflect.DeepEqual(movie.AdditionalParts, wantParts) {
				t.Fatalf("stale parts got=%v want=%v", movie.AdditionalParts, wantParts)
			}
			gotActors, err := s.Actors(id)
			if err != nil || !reflect.DeepEqual(gotActors, actors) {
				t.Fatalf("manual actors lost: %v %v", gotActors, err)
			}
			gotData, err := s.DataUser(1, id)
			if err != nil || !reflect.DeepEqual(gotData, userdata) {
				t.Fatalf("manual userdata lost: %+v %v", gotData, err)
			}
			var triggers []string
			for _, table := range []string{"movies", "actors", "movie_actors", "movie_features", "scan_groups", "scan_group_sources"} {
				for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
					name := fmt.Sprintf("manual_parts_reject_%s_%s", table, op)
					stmt := fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'unexpected unchanged manual DML'); END", name, op, table)
					if _, err := db.Exec(stmt); err != nil {
						t.Fatal(err)
					}
					triggers = append(triggers, name)
				}
			}
			version := s.Version("g:version")
			result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
			if err != nil || result.UnchangedGroups != 1 || result.IndexChanged || !result.CleanupPerformed || s.Version("g:version") != version {
				t.Fatalf("manual steady state is not zero-write: %+v %v version=%s/%s", result, err, version, s.Version("g:version"))
			}
			for _, name := range triggers {
				if _, err := db.Exec("DROP TRIGGER " + name); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// The test uses only disposable local roots and the same aborting SQLite
// instrumentation as TestIncrementalNoBusinessOrStateDML.
