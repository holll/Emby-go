package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/regularfile"
	"emby-go/internal/store"
)

func TestSnapshotGrowthPreservesReadError(t *testing.T) {
	for _, stage := range []string{"before-first-check", "after-build"} {
		for _, kind := range []string{"source", "nfo", "missing-nfo", "changed-source", "new-nfo", "directory-nfo"} {
			t.Run(stage+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				source := filepath.Join(root, "Film-CD1.strm")
				meta := filepath.Join(root, "Film.nfo")
				scanWrite(t, source, "https://media.test/1.mp4\n")
				scanWrite(t, filepath.Join(root, "Film-CD2.strm"), "https://media.test/2.mp4\n")
				if kind != "missing-nfo" && kind != "new-nfo" {
					scanWrite(t, meta, "<movie><title>Original</title></movie>")
				}
				lib := store.Library{Path: root}
				groups, err := enumerateGroupsContext(context.Background(), lib)
				if err != nil {
					t.Fatal(err)
				}
				for key, group := range groups {
					inputs, err := readGroupInputs(lib, key, group)
					if err != nil {
						t.Fatal(err)
					}
					if err := inputs.stillMatches(); err != nil {
						t.Fatalf("unchanged snapshot: %v", err)
					}
					if stage == "after-build" {
						if _, err := buildRecords(lib, inputs, nil); err != nil {
							t.Fatal(err)
						}
					}
					want := nfo.ErrTooLarge
					switch kind {
					case "source":
						scanWrite(t, source, strings.Repeat("x", MaxSourceSize+1))
						want = ErrSourceTooLarge
					case "nfo", "missing-nfo":
						scanWrite(t, meta, strings.Repeat("x", nfo.MaxFileSize+1))
					case "changed-source":
						scanWrite(t, source, "https://media.test/changed.mp4\n")
						want = ErrScanIncomplete
					case "new-nfo":
						scanWrite(t, meta, "<movie/>")
						want = ErrScanIncomplete
					case "directory-nfo":
						if err := os.Remove(meta); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(meta, 0755); err != nil {
							t.Fatal(err)
						}
						want = regularfile.ErrNotRegular
					}
					err = inputs.stillMatches()
					if !errors.Is(err, want) {
						t.Fatalf("got %v, want %v", err, want)
					}
				}
			})
		}
	}
}

// ScanWithOptions reports progress(0) before the group loop. The loop's first
// Err call precedes readGroupInputs; its second is health(), between the two
// stillMatches checks. Mutating there deterministically exercises growth after
// the first successful check, without a production hook or racing a file writer.
type growthWindowContext struct {
	context.Context
	armed  atomic.Bool
	checks atomic.Int32
	grow   func()
}

func (ctx *growthWindowContext) Err() error {
	if ctx.armed.Load() && ctx.checks.Add(1) == 2 {
		ctx.grow()
	}
	return ctx.Context.Err()
}

func TestScanGrowthWindowClassification(t *testing.T) {
	for _, mode := range []ScanMode{ScanModeFull, ScanModeIncremental} {
		for _, kind := range []string{"source", "nfo", "missing-nfo", "changed-source"} {
			for _, healthy := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/healthy=%t", mode, kind, healthy), func(t *testing.T) {
					root := t.TempDir()
					source := filepath.Join(root, "A-CD1.strm")
					part := filepath.Join(root, "A-CD2.strm")
					meta := filepath.Join(root, "A.nfo")
					scanWrite(t, source, "https://media.test/1.mp4\n")
					scanWrite(t, part, "https://media.test/2.mp4\n")
					if kind != "missing-nfo" {
						scanWrite(t, meta, "<movie><title>Original</title><actor><name>Actor</name></actor></movie>")
					}
					orphan := filepath.Join(root, "Z.strm")
					scanWrite(t, orphan, "https://media.test/z.mp4\n")
					s := newStore(t, root)
					lib, err := s.AddLibrary("L", root)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := Scan(s, lib); err != nil {
						t.Fatal(err)
					}
					id, err := s.MovieIDByPath(source)
					if err != nil {
						t.Fatal(err)
					}
					if kind != "missing-nfo" {
						if err := s.SavePlaybackForUser(1, id, 7654321, 3, "2026-11-06T00:00:00Z", 7654321); err != nil {
							t.Fatal(err)
						}
						data, err := s.SetIndexedFavoriteDataForUser(1, id, true)
						if err != nil || !data.IsFavorite || data.PositionTicks != 7654321 || data.PlayCount != 3 {
							t.Fatalf("nonempty personal baseline: %+v %v", data, err)
						}
					}
					userData, err := s.DataUser(1, id)
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
					libVersion := s.Version(fmt.Sprintf("lib:%d:version", lib.ID))
					// Prevent the incremental fast path from skipping the second check.
					scanWrite(t, source, "https://media.test/updated-1.mp4\n")
					if err := os.Remove(orphan); err != nil {
						t.Fatal(err)
					}
					if healthy {
						// CD group keys are lowercased; keep this standalone key
						// lowercase too so it sorts after the target group "a".
						scanWrite(t, filepath.Join(root, "b.strm"), "https://media.test/b.mp4\n")
						scanWrite(t, filepath.Join(root, "b.nfo"), "<movie><title>Healthy</title></movie>")
					}
					mutated := false
					ctx := &growthWindowContext{Context: context.Background(), grow: func() {
						mutated = true
						switch kind {
						case "source":
							scanWrite(t, part, strings.Repeat("x", MaxSourceSize+1))
						case "nfo", "missing-nfo":
							scanWrite(t, meta, strings.Repeat("x", nfo.MaxFileSize+1))
						case "changed-source":
							scanWrite(t, part, "https://media.test/changed-2.mp4\n")
						}
					}}
					var log bytes.Buffer
					previousLogger := slog.Default()
					slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
					defer slog.SetDefault(previousLogger)
					result, err := ScanWithOptions(ctx, s, lib, ScanOptions{Mode: mode}, func(p Progress) {
						if p.Done == 0 {
							ctx.armed.Store(true)
						}
					})
					if !mutated || !result.Incomplete || result.CleanupPerformed || result.Deleted != 0 || result.GroupsDone != result.GroupsTotal {
						t.Fatalf("mutation=%t result=%+v err=%v", mutated, result, err)
					}
					if kind == "changed-source" {
						if !errors.Is(err, ErrScanIncomplete) || result.Failed != 1 || result.Skipped != 0 {
							t.Fatalf("ordinary change was not failed: %+v %v", result, err)
						}
					} else if err != nil || result.Failed != 0 || result.Skipped != 2 || !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "超限") {
						t.Fatalf("growth not skipped with WARN: %+v %v logs=%s", result, err, log.String())
					}
					after, err := s.Movie(id)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("movie changed: before=%+v after=%+v err=%v", before, after, err)
					}
					afterData, err := s.DataUser(1, id)
					if err != nil || !reflect.DeepEqual(userData, afterData) {
						t.Fatalf("growth changed personal state: before=%+v after=%+v err=%v", userData, afterData, err)
					}
					actorsAfter, err := s.Actors(id)
					if err != nil || !reflect.DeepEqual(actorsBefore, actorsAfter) {
						t.Fatalf("actors changed: %v", err)
					}
					statesAfter, err := s.LoadScanGroups(lib.ID)
					if err != nil {
						t.Fatal(err)
					}
					for key, state := range statesBefore {
						if !reflect.DeepEqual(state, statesAfter[key]) {
							t.Fatalf("skipped/orphan baseline changed for %s", key)
						}
					}
					if _, err := s.MovieIDByPath(orphan); err != nil {
						t.Fatalf("partial inventory reconciled orphan: %v", err)
					}
					if healthy {
						bid, err := s.MovieIDByPath(filepath.Join(root, "b.strm"))
						if err != nil {
							t.Fatal(err)
						}
						movie, err := s.Movie(bid)
						if err != nil || movie.Title != "Healthy" || result.Success != 1 {
							t.Fatalf("healthy group did not continue: %+v %+v %v", result, movie, err)
						}
					} else if version != s.Version("g:version") || libVersion != s.Version(fmt.Sprintf("lib:%d:version", lib.ID)) || len(statesBefore) != len(statesAfter) {
						t.Fatal("skipped-only scan changed versions/baseline")
					}
					// Close the recovery gap on four existing representative windows,
					// rather than creating another growth-case matrix.
					if healthy && (kind == "source" || kind == "nfo") {
						scanWrite(t, part, "https://media.test/2.mp4\n")
						scanWrite(t, meta, "<movie><title>Repaired</title><actor><name>Actor</name></actor></movie>")
						recovered, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: mode}, nil)
						wantSuccess, wantUnchanged := 2, 0
						if mode == ScanModeIncremental {
							wantSuccess, wantUnchanged = 1, 1
						}
						if err != nil || recovered.Incomplete || !recovered.CleanupPerformed || recovered.Deleted != 1 || recovered.Success != wantSuccess || recovered.UnchangedGroups != wantUnchanged {
							t.Fatalf("repaired scan: %+v %v", recovered, err)
						}
						after, err := s.Movie(id)
						if err != nil || after.Title != "Repaired" || !reflect.DeepEqual(before.AdditionalParts, after.AdditionalParts) {
							t.Fatalf("recovered movie/parts: %+v %v", after, err)
						}
						afterData, err := s.DataUser(1, id)
						if err != nil || !reflect.DeepEqual(userData, afterData) {
							t.Fatalf("recovery changed personal state: %+v %v", afterData, err)
						}
						afterActors, err := s.Actors(id)
						if err != nil || !reflect.DeepEqual(actorsBefore, afterActors) {
							t.Fatalf("recovered actors: %+v %v", afterActors, err)
						}
						if _, err := s.MovieIDByPath(orphan); !store.NotFound(err) {
							t.Fatalf("complete inventory retained orphan: %v", err)
						}
					}
				})
			}
		}
	}
}
