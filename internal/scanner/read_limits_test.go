package scanner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

func TestReadSourceFileLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.strm")
	for _, tc := range []struct {
		name string
		text string
		want string
		err  error
	}{
		{"empty", "", "", io.EOF},
		{"spaces", "  ", "", io.EOF},
		{"empty-line", "\n", "", nil},
		{"trim-first", " https://media.test/a.mp4 \r\nignored", "https://media.test/a.mp4", nil},
		{"exact", "https://media.test/a.mp4\n" + strings.Repeat(" ", MaxSourceSize-len("https://media.test/a.mp4\n")), "https://media.test/a.mp4", nil},
		{"over-one", "https://media.test/a.mp4\n" + strings.Repeat(" ", MaxSourceSize+1-len("https://media.test/a.mp4\n")), "", ErrSourceTooLarge},
		{"long-first-line", strings.Repeat("x", MaxSourceSize+1), "", ErrSourceTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.text), 0644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadSource(path)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = readMemberInput(candidate{path: path, info: info})
			if errors.Is(tc.err, ErrSourceTooLarge) && !errors.Is(err, ErrSourceTooLarge) {
				t.Fatalf("fingerprint bypassed bound: %v", err)
			}
		})
	}
	if _, err := ReadSource(filepath.Join(t.TempDir(), "missing.strm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := ReadSource(t.TempDir()); err == nil {
		t.Fatal("directory read succeeded")
	}
}

func TestScanSkipsOversizedFilesPreservingIndex(t *testing.T) {
	for _, mode := range []string{"full", "incremental", "rescan", "reindex"} {
		for _, kind := range []string{"strm", "nfo"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				root, s, lib := scanFixture(t)
				source := filepath.Join(root, "A.strm")
				id, err := s.MovieIDByPath(source)
				if err != nil {
					t.Fatal(err)
				}
				before, err := s.Movie(id)
				if err != nil {
					t.Fatal(err)
				}
				actorsBefore, err := s.Actors(id)
				if err != nil {
					t.Fatal(err)
				}
				statesBefore, err := s.LoadScanGroups(lib.ID)
				if err != nil {
					t.Fatal(err)
				}
				version := s.Version("g:version")
				path := source
				raw := []byte("https://media.test/a.mp4\n" + strings.Repeat(" ", MaxSourceSize))
				if kind == "nfo" {
					path = filepath.Join(root, "A.nfo")
					raw = []byte("<movie><title>Do not index</title><plot>" + strings.Repeat("x", nfo.MaxFileSize) + "</plot></movie>")
				}
				if err := os.WriteFile(path, raw, 0644); err != nil {
					t.Fatal(err)
				}
				var log bytes.Buffer
				previousLogger := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
				defer slog.SetDefault(previousLogger)
				var result Result
				switch mode {
				case "full":
					result, err = Scan(s, lib)
				case "incremental":
					result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
				case "rescan":
					result, err = RescanOne(s, lib, source)
				case "reindex":
					result, err = ReindexOne(s, lib, source)
				}
				if err != nil || result.Skipped != 1 || result.Failed != 0 || !result.Incomplete || result.CleanupPerformed || len(result.IndexedSources) != 0 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "超限") {
					t.Fatalf("no oversized warning: %s", log.String())
				}
				after, err := s.Movie(id)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("movie changed: before=%+v after=%+v err=%v", before, after, err)
				}
				actorsAfter, err := s.Actors(id)
				if err != nil || !reflect.DeepEqual(actorsBefore, actorsAfter) {
					t.Fatalf("actors changed: %v", err)
				}
				statesAfter, err := s.LoadScanGroups(lib.ID)
				if err != nil || !reflect.DeepEqual(statesBefore, statesAfter) || version != s.Version("g:version") {
					t.Fatalf("baseline/version changed: %v", err)
				}
				actual, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(actual, raw) {
					t.Fatalf("source file changed: %v", err)
				}
			})
		}
	}
}

func TestScanOversizedStackDependency(t *testing.T) {
	for _, mode := range []string{"full", "incremental", "rescan", "reindex"} {
		for _, dependency := range []string{"Film-CD2.strm", "Film.nfo", "shared-with-sidecar", "sidecar-with-shared"} {
			t.Run(mode+"/"+dependency, func(t *testing.T) {
				root := t.TempDir()
				for _, name := range []string{"Film-CD1.strm", "Film-CD2.strm"} {
					scanWrite(t, filepath.Join(root, name), "https://media.test/"+name+".mp4\n")
				}
				scanWrite(t, filepath.Join(root, "Film.nfo"), "<movie><title>Stack</title></movie>")
				if dependency == "shared-with-sidecar" || dependency == "sidecar-with-shared" {
					scanWrite(t, filepath.Join(root, "Film-CD1.nfo"), "<movie><title>Primary</title></movie>")
				}
				s := newStore(t, root)
				lib, err := s.AddLibrary("L", root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Scan(s, lib); err != nil {
					t.Fatal(err)
				}
				source := filepath.Join(root, "Film-CD1.strm")
				id, err := s.MovieIDByPath(source)
				if err != nil {
					t.Fatal(err)
				}
				before, err := s.Movie(id)
				if err != nil {
					t.Fatal(err)
				}
				dependencyPath := dependency
				switch dependency {
				case "shared-with-sidecar":
					dependencyPath = "Film.nfo"
				case "sidecar-with-shared":
					dependencyPath = "Film-CD1.nfo"
				}
				limit := MaxSourceSize
				if strings.HasSuffix(dependencyPath, ".nfo") {
					limit = nfo.MaxFileSize
				}
				scanWrite(t, filepath.Join(root, dependencyPath), strings.Repeat("x", limit+1))
				version := s.Version("g:version")
				var result Result
				switch mode {
				case "full":
					result, err = Scan(s, lib)
				case "incremental":
					result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
				case "rescan":
					result, err = RescanOne(s, lib, source)
				case "reindex":
					result, err = ReindexOne(s, lib, source)
				}
				after, readErr := s.Movie(id)
				if err != nil || readErr != nil || result.Skipped != 2 || result.Failed != 0 || !result.Incomplete || result.CleanupPerformed || !reflect.DeepEqual(before, after) || version != s.Version("g:version") {
					t.Fatalf("dependency bypassed bound: result=%+v err=%v readErr=%v before=%+v after=%+v", result, err, readErr, before, after)
				}
			})
		}
	}
}

func TestScanOversizedGroupContinuesWithoutCleanup(t *testing.T) {
	for _, mode := range []ScanMode{ScanModeFull, ScanModeIncremental} {
		t.Run(string(mode), func(t *testing.T) {
			root, s, lib := scanFixture(t)
			id, err := s.MovieIDByPath(filepath.Join(root, "A.strm"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "A.nfo"), []byte(strings.Repeat("x", nfo.MaxFileSize+1)), 0644); err != nil {
				t.Fatal(err)
			}
			for name, text := range map[string]string{"B.strm": "https://media.test/b.mp4\n", "B.nfo": "<movie><title>Healthy</title></movie>"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0644); err != nil {
					t.Fatal(err)
				}
			}
			result, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: mode}, nil)
			if err != nil || result.Skipped != 1 || result.Success != 1 || result.Failed != 0 || !result.Incomplete || result.CleanupPerformed || result.Deleted != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, err := s.Movie(id); err != nil {
				t.Fatalf("skipped row removed by reconciliation: %v", err)
			}
			bid, err := s.MovieIDByPath(filepath.Join(root, "B.strm"))
			if err != nil {
				t.Fatal(err)
			}
			movie, err := s.Movie(bid)
			if err != nil || movie.Title != "Healthy" {
				t.Fatalf("healthy group not indexed: %+v %v", movie, err)
			}
			if err := os.WriteFile(filepath.Join(root, "A.nfo"), []byte("<movie><title>Recovered</title></movie>"), 0644); err != nil {
				t.Fatal(err)
			}
			result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: mode}, nil)
			if err != nil || result.Incomplete || !result.CleanupPerformed || result.Skipped != 0 {
				t.Fatalf("subsequent independent scan did not recover: %+v %v", result, err)
			}
			movie, err = s.Movie(id)
			if err != nil || movie.Title != "Recovered" {
				t.Fatalf("recovered group not indexed: %+v %v", movie, err)
			}
		})
	}
}

func TestSingleScanOversizedIndependentCDMember(t *testing.T) {
	for _, mode := range []string{"rescan", "reindex"} {
		for _, firstSource := range []string{"https://media.test/new.mp4\n", "ed2k://invalid\n"} {
			for _, oversized := range []string{"Film-CD3.strm", "Film-CD3.nfo"} {
				t.Run(mode+"/"+firstSource[:5]+"/"+oversized, func(t *testing.T) {
					root := t.TempDir()
					for _, base := range []string{"Film-CD2", "Film-CD3"} {
						scanWrite(t, filepath.Join(root, base+".strm"), "https://media.test/old.mp4\n")
						scanWrite(t, filepath.Join(root, base+".nfo"), "<movie><title>Old</title></movie>")
					}
					s := newStore(t, root)
					lib, err := s.AddLibrary("L", root)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := Scan(s, lib); err != nil {
						t.Fatal(err)
					}
					first := filepath.Join(root, "Film-CD2.strm")
					second := filepath.Join(root, "Film-CD3.strm")
					secondID, err := s.MovieIDByPath(second)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.SetAutoMarks(secondID, []string{"Owned"}); err != nil {
						t.Fatal(err)
					}
					before, err := s.Movie(secondID)
					if err != nil {
						t.Fatal(err)
					}
					scanWrite(t, first, firstSource)
					scanWrite(t, filepath.Join(root, "Film-CD2.nfo"), "<movie><title>Updated</title></movie>")
					limit := MaxSourceSize
					if strings.HasSuffix(oversized, ".nfo") {
						limit = nfo.MaxFileSize
					}
					scanWrite(t, filepath.Join(root, oversized), strings.Repeat("x", limit+1))
					var result Result
					if mode == "rescan" {
						result, err = RescanOne(s, lib, first)
					} else {
						result, err = ReindexOne(s, lib, first)
					}
					wantSkipped := 1
					if strings.HasPrefix(firstSource, "ed2k") {
						wantSkipped++
					}
					if err != nil || result.Failed != 0 || result.Skipped != wantSkipped || !result.Incomplete {
						t.Fatalf("result=%+v err=%v", result, err)
					}
					after, err := s.Movie(secondID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("oversized independent member changed: %+v %+v %v", before, after, err)
					}
					firstID, firstErr := s.MovieIDByPath(first)
					if strings.HasPrefix(firstSource, "https") {
						movie, err := s.Movie(firstID)
						if firstErr != nil || err != nil || movie.Title != "Updated" || !reflect.DeepEqual(result.IndexedSources, []string{first}) {
							t.Fatalf("healthy independent row not updated: %+v %+v %v %v", result, movie, firstErr, err)
						}
					} else if mode == "rescan" {
						if !store.NotFound(firstErr) {
							t.Fatalf("non-HTTP row retained: %v", firstErr)
						}
					} else if firstErr != nil {
						t.Fatalf("non-destructive reindex removed row: %v", firstErr)
					}
				})
			}
		}
	}
}
