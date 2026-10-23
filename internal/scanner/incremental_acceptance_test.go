package scanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/store"
)

func scanWrite(t testing.TB, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func scanDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "test.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func scanFixture(t *testing.T) (string, *store.Store, store.Library) {
	t.Helper()
	root := t.TempDir()
	scanWrite(t, filepath.Join(root, "A.strm"), "https://media.test/a.mp4\n")
	scanWrite(t, filepath.Join(root, "A.nfo"), "<movie><title>Alpha</title><actor><name>Actor</name></actor></movie>")
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	return root, s, lib
}

func TestIncrementalNoBusinessOrStateDML(t *testing.T) {
	root, s, lib := scanFixture(t)
	db := scanDB(t, root)
	// Abort any accidental business or baseline DML, not just version writes.
	for _, table := range []string{"movies", "actors", "movie_actors", "movie_features", "scan_groups", "scan_group_sources"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			stmt := fmt.Sprintf("CREATE TRIGGER reject_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'unexpected unchanged DML'); END", table, op, op, table)
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := s.Version("g:version")
	got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnchangedGroups != 1 || got.IndexChanged || !got.CleanupPerformed || s.Version("g:version") != before {
		t.Fatalf("unexpected unchanged result: %+v versions %s/%s", got, before, s.Version("g:version"))
	}
}

func TestIncrementalNFOSameStatAndLazyDecode(t *testing.T) {
	root, s, lib := scanFixture(t)
	path := filepath.Join(root, "A.nfo")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	scanWrite(t, path, "<movie><title>Bravo</title><actor><name>Actor</name></actor></movie>")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil || got.Updated != 1 {
		t.Fatalf("NFO overwrite: %+v %v", got, err)
	}
	id, err := s.MovieIDByPath(filepath.Join(root, "A.strm"))
	if err != nil {
		t.Fatal(err)
	}
	movie, err := s.Movie(id)
	if err != nil || movie.Title != "Bravo" {
		t.Fatalf("movie %+v %v", movie, err)
	}
	groups, err := enumerateGroups(lib)
	if err != nil {
		t.Fatal(err)
	}
	for key, group := range groups {
		inputs, err := readGroupInputs(lib, key, group)
		if err != nil {
			t.Fatal(err)
		}
		if inputs.records[0].movie.Title != "" {
			t.Fatal("snapshot phase decoded NFO")
		}
		for _, input := range inputs.nfos {
			if input.meta.Title != "" {
				t.Fatal("snapshot phase parsed NFO")
			}
		}
	}
}

func TestIncrementalFinalSafetyChecks(t *testing.T) {
	for _, scenario := range []string{"cancel", "config", "root", "inventory"} {
		t.Run(scenario, func(t *testing.T) {
			root, s, lib := scanFixture(t)
			db := scanDB(t, root)
			// A stale row must not be reconciled after an unsafe final callback.
			stale := store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "missing.strm"), Status: "pending"}
			if _, err := s.IndexMovie(stale, 0, time.Time{}, nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, err := ScanWithOptions(ctx, s, lib, ScanOptions{Mode: ScanModeIncremental}, func(p Progress) {
				if p.Done != p.Total || p.Done == 0 {
					return
				}
				switch scenario {
				case "cancel":
					cancel()
				case "config":
					if _, e := db.Exec("UPDATE libraries SET path=? WHERE id=?", t.TempDir(), lib.ID); e != nil {
						t.Fatal(e)
					}
				case "root":
					// Replace only an empty disposable child root after renaming the original.
					backup := root + "-held"
					if e := os.Rename(root, backup); e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() {
						if e := os.Remove(root); e != nil {
							t.Error(e)
						}
						if e := os.Rename(backup, root); e != nil {
							t.Error(e)
						}
					})
					if e := os.Mkdir(root, 0755); e != nil {
						t.Fatal(e)
					}
				case "inventory":
					scanWrite(t, filepath.Join(root, "new.strm"), "https://media.test/new.mp4")
				}
			})
			if err == nil || !got.Incomplete || got.CleanupPerformed {
				t.Fatalf("unsafe scan: %+v %v", got, err)
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if scenario != "root" {
				if _, e := s.MovieIDByPath(stale.SourcePath); e != nil {
					t.Fatalf("stale row deleted: %v", e)
				}
			}
		})
	}
}

func TestIncrementalSnapshotStability(t *testing.T) {
	for _, scenario := range []string{"source", "nfo", "image", "member"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "Film-CD1.strm")
			scanWrite(t, source, "https://media.test/a.mp4")
			scanWrite(t, filepath.Join(root, "Film-CD2.strm"), "https://media.test/2.mp4")
			lib := store.Library{Path: root}
			groups, err := enumerateGroups(lib)
			if err != nil {
				t.Fatal(err)
			}
			for key, group := range groups {
				inputs, err := readGroupInputs(lib, key, group)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "source":
					info, e := os.Stat(source)
					if e != nil {
						t.Fatal(e)
					}
					scanWrite(t, source, "https://media.test/b.mp4")
					if e := os.Chtimes(source, info.ModTime(), info.ModTime()); e != nil {
						t.Fatal(e)
					}
				case "nfo":
					scanWrite(t, filepath.Join(root, "Film-CD1.nfo"), "<movie/>")
				case "image":
					scanWrite(t, filepath.Join(root, "poster.webp"), "image")
				case "member":
					scanWrite(t, filepath.Join(root, "Film-CD3.strm"), "https://media.test/3.mp4")
				}
				if inputs.stillMatches() {
					t.Fatal("changed dependency accepted")
				}
			}
		})
	}
}

func TestIncrementalRestartRepairsPartialScan(t *testing.T) {
	root, s, lib := scanFixture(t)
	scanWrite(t, filepath.Join(root, "B.strm"), "https://media.test/b.mp4")
	scanWrite(t, filepath.Join(root, "B.nfo"), "<movie><title>B</title></movie>")
	ctx, cancel := context.WithCancel(context.Background())
	got, err := ScanWithOptions(ctx, s, lib, ScanOptions{Mode: ScanModeIncremental}, func(p Progress) {
		if p.Done == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || !got.Incomplete {
		t.Fatalf("partial: %+v %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = ScanWithOptions(context.Background(), reopened, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil || got.UnchangedGroups != 1 || got.Added != 1 {
		t.Fatalf("restart repair: %+v %v", got, err)
	}
	got, err = ScanWithOptions(context.Background(), reopened, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if err != nil || got.UnchangedGroups != 2 || got.IndexChanged {
		t.Fatalf("steady restart: %+v %v", got, err)
	}
}
