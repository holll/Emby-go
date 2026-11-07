//go:build unix

package scanner

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/regularfile"

	"golang.org/x/sys/unix"
)

// A regression in FIFO opening must fail this child, not hang the entire suite.
// The deadline is a test watchdog, not a filesystem-operation time guarantee.
func TestScanSpecialFiles(t *testing.T) {
	if os.Getenv("EMBY_SCAN_SPECIAL_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestScanSpecialFiles$", "-test.count=1")
		cmd.Env = append(os.Environ(), "EMBY_SCAN_SPECIAL_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("special-file child: %v context=%v\n%s", err, ctx.Err(), output)
		}
		return
	}

	for _, mode := range []string{"full", "incremental", "rescan", "reindex", "full-healthy", "incremental-healthy"} {
		for _, dependency := range []string{"source", "nfo", "symlink-fifo", "cd2", "shared-with-sidecar", "sidecar-with-shared"} {
			t.Run(mode+"/"+dependency, func(t *testing.T) {
				root := t.TempDir()
				stack := dependency == "cd2" || strings.Contains(dependency, "with-")
				source := filepath.Join(root, "A.strm")
				meta := filepath.Join(root, "A.nfo")
				if stack {
					source = filepath.Join(root, "A-CD1.strm")
					scanWrite(t, filepath.Join(root, "A-CD2.strm"), "https://media.test/2.mp4\n")
				}
				scanWrite(t, source, "https://media.test/1.mp4\n")
				scanWrite(t, meta, "<movie><title>Original</title><tag>manual</tag><actor><name>Actor</name></actor></movie>")
				if strings.Contains(dependency, "with-") {
					scanWrite(t, filepath.Join(root, "A-CD1.nfo"), "<movie><title>Sidecar</title><tag>manual</tag><actor><name>Actor</name></actor></movie>")
				}
				orphan := filepath.Join(root, "Z.strm")
				scanWrite(t, orphan, "https://media.test/orphan.mp4\n")
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
				if err := s.SetAutoMarks(id, []string{"owned"}); err != nil {
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
				states, err := s.LoadScanGroups(lib.ID)
				if err != nil {
					t.Fatal(err)
				}
				version := s.Version("g:version")
				libVersion := s.Version(fmt.Sprintf("lib:%d:version", lib.ID))
				if err := os.Remove(orphan); err != nil {
					t.Fatal(err)
				}
				healthy := strings.HasSuffix(mode, "-healthy")
				if healthy {
					scanWrite(t, filepath.Join(root, "b.strm"), "https://media.test/b.mp4\n")
					scanWrite(t, filepath.Join(root, "b.nfo"), "<movie><title>Healthy</title></movie>")
				}
				path := source
				switch dependency {
				case "nfo", "shared-with-sidecar":
					path = meta
				case "cd2":
					path = filepath.Join(root, "A-CD2.strm")
				case "sidecar-with-shared":
					path = filepath.Join(root, "A-CD1.nfo")
				}
				// path is an explicit fixture source/NFO under this private root.
				if filepath.Dir(path) != root {
					t.Fatal("unexpected replacement path")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				fifo := path
				if dependency == "symlink-fifo" {
					fifo = filepath.Join(root, "input.pipe")
					if err := os.Symlink(fifo, path); err != nil {
						t.Fatal(err)
					}
				}
				if err := unix.Mkfifo(fifo, 0600); err != nil {
					t.Fatal(err)
				}
				var logs bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				defer slog.SetDefault(previous)
				var result Result
				scanMode := strings.TrimSuffix(mode, "-healthy")
				switch scanMode {
				case "full", "incremental":
					result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanMode(scanMode)}, nil)
				case "rescan":
					result, err = RescanOne(s, lib, source)
				case "reindex":
					result, err = ReindexOne(s, lib, source)
				}
				wantSkipped := 1
				if stack {
					wantSkipped = 2
				}
				if err != nil || result.Skipped != wantSkipped || result.Failed != 0 || result.UnreadableNFO != 0 || !result.Incomplete || result.CleanupPerformed || len(result.IndexedSources) != 0 {
					t.Fatalf("special input result=%+v err=%v", result, err)
				}
				if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "非普通") {
					t.Fatalf("missing special-file warning: %s", logs.String())
				}
				after, err := s.Movie(id)
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("special input changed movie: before=%+v after=%+v err=%v", before, after, err)
				}
				actorsAfter, err := s.Actors(id)
				if err != nil || !reflect.DeepEqual(actorsAfter, actors) {
					t.Fatalf("special input changed actors: %v", err)
				}
				statesAfter, err := s.LoadScanGroups(lib.ID)
				if err != nil {
					t.Fatal(err)
				}
				for key, state := range states {
					if !reflect.DeepEqual(state, statesAfter[key]) {
						t.Fatalf("special input changed baseline for %s", key)
					}
				}
				if _, err := s.MovieIDByPath(orphan); err != nil {
					t.Fatalf("incomplete inventory removed orphan: %v", err)
				}
				if healthy {
					bid, err := s.MovieIDByPath(filepath.Join(root, "b.strm"))
					if err != nil {
						t.Fatal(err)
					}
					movie, err := s.Movie(bid)
					if err != nil || movie.Title != "Healthy" || result.Success != 1 {
						t.Fatalf("healthy group did not continue: %+v %v", movie, err)
					}
				} else if !reflect.DeepEqual(statesAfter, states) || version != s.Version("g:version") || libVersion != s.Version(fmt.Sprintf("lib:%d:version", lib.ID)) {
					t.Fatal("special input changed baseline/version")
				}
			})
		}
	}

	for _, mode := range []ScanMode{ScanModeFull, ScanModeIncremental} {
		for _, dependency := range []string{"source", "nfo", "missing-nfo"} {
			t.Run(string(mode)+"/replacement-window/"+dependency, func(t *testing.T) {
				root, s, lib := scanFixture(t)
				source := filepath.Join(root, "A.strm")
				meta := filepath.Join(root, "A.nfo")
				orphan := filepath.Join(root, "Z.strm")
				healthy := filepath.Join(root, "b.strm")
				wantSkipped := 1
				if dependency != "missing-nfo" {
					// Give parts preservation a real member, not just an empty slice.
					stackSource := filepath.Join(root, "A-CD1.strm")
					if err := os.Rename(source, stackSource); err != nil {
						t.Fatal(err)
					}
					source = stackSource
					scanWrite(t, filepath.Join(root, "A-CD2.strm"), "https://media.test/part2.mp4\n")
					scanWrite(t, orphan, "https://media.test/orphan.mp4\n")
					if _, err := Scan(s, lib); err != nil {
						t.Fatal(err)
					}
					wantSkipped = 2
				}
				id, err := s.MovieIDByPath(source)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.SetAutoMarks(id, []string{"owned"}); err != nil {
					t.Fatal(err)
				}
				before, err := s.Movie(id)
				if err != nil {
					t.Fatal(err)
				}
				states, err := s.LoadScanGroups(lib.ID)
				if err != nil {
					t.Fatal(err)
				}
				if dependency != "missing-nfo" {
					if err := s.SavePlaybackForUser(1, id, 123000000, 3, "2026-01-02T03:04:05Z", 123000000); err != nil {
						t.Fatal(err)
					}
					if _, err := s.SetIndexedFavoriteDataForUser(1, id, true); err != nil {
						t.Fatal(err)
					}
					if len(before.AdditionalParts) != 1 {
						t.Fatalf("missing baseline part: %+v", before)
					}
					if err := os.Remove(orphan); err != nil {
						t.Fatal(err)
					}
					scanWrite(t, healthy, "https://media.test/healthy.mp4\n")
					scanWrite(t, filepath.Join(root, "b.nfo"), "<movie><title>Healthy</title></movie>")
				}
				userBefore, err := s.DataUser(1, id)
				if err != nil {
					t.Fatal(err)
				}
				actors, err := s.Actors(id)
				if err != nil {
					t.Fatal(err)
				}
				version := s.Version("g:version")
				// Force incremental to run its second check rather than reuse.
				scanWrite(t, source, "https://media.test/changed.mp4\n")
				if dependency == "missing-nfo" {
					if err := os.Remove(meta); err != nil {
						t.Fatal(err)
					}
				}
				mutated := false
				ctx := &growthWindowContext{Context: context.Background(), grow: func() {
					mutated = true
					path := meta
					if dependency == "source" {
						path = source
					}
					if dependency != "missing-nfo" {
						if filepath.Dir(path) != root {
							t.Fatal("unexpected replacement path")
						}
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					if err := unix.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				}}
				result, err := ScanWithOptions(ctx, s, lib, ScanOptions{Mode: mode}, func(p Progress) {
					if p.Done == 0 {
						ctx.armed.Store(true)
					}
				})
				if !mutated || err != nil || result.Skipped != wantSkipped || result.Failed != 0 || result.UnreadableNFO != 0 || !result.Incomplete || result.CleanupPerformed {
					t.Fatalf("replacement classified incorrectly: mutated=%v result=%+v err=%v", mutated, result, err)
				}
				after, err := s.Movie(id)
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("replacement changed movie: %+v %v", after, err)
				}
				statesAfter, err := s.LoadScanGroups(lib.ID)
				if err != nil {
					t.Fatal(err)
				}
				if dependency == "missing-nfo" {
					if !reflect.DeepEqual(statesAfter, states) || version != s.Version("g:version") {
						t.Fatal("replacement changed baseline/version")
					}
					return
				}
				for key, state := range states {
					if !reflect.DeepEqual(state, statesAfter[key]) {
						t.Fatalf("replacement changed baseline for %s", key)
					}
				}
				actorsAfter, err := s.Actors(id)
				if err != nil || !reflect.DeepEqual(actorsAfter, actors) {
					t.Fatalf("replacement changed actors: %v", err)
				}
				userAfter, err := s.DataUser(1, id)
				if err != nil || !reflect.DeepEqual(userAfter, userBefore) || userAfter.PositionTicks != 123000000 || userAfter.PlayCount != 3 || !userAfter.IsFavorite {
					t.Fatalf("replacement changed personal state: before=%+v after=%+v err=%v", userBefore, userAfter, err)
				}
				if _, err := s.MovieIDByPath(orphan); err != nil {
					t.Fatalf("incomplete replacement removed orphan: %v", err)
				}
				healthyID, err := s.MovieIDByPath(healthy)
				if err != nil {
					t.Fatal(err)
				}
				healthyMovie, err := s.Movie(healthyID)
				if err != nil || healthyMovie.Title != "Healthy" || result.Success != 1 || result.Added != 1 || result.GroupsDone != 2 {
					t.Fatalf("healthy group did not continue: movie=%+v result=%+v err=%v", healthyMovie, result, err)
				}

				path := meta
				if dependency == "source" {
					path = source
				}
				// Remove only the FIFO installed by this private replacement window.
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				scanWrite(t, source, "https://media.test/a.mp4\n")
				scanWrite(t, meta, "<movie><title>Recovered replacement</title><actor><name>Actor</name></actor></movie>")
				recovered, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: mode}, nil)
				if err != nil || recovered.Mode != string(mode) || recovered.Incomplete || !recovered.CleanupPerformed || recovered.Skipped != 0 || recovered.Failed != 0 || recovered.UnreadableNFO != 0 || recovered.Pending != 0 || recovered.Deleted != 1 || recovered.GroupsTotal != 2 || recovered.GroupsDone != 2 || !recovered.IndexChanged {
					t.Fatalf("replacement recovery incomplete: result=%+v err=%v", recovered, err)
				}
				wantSuccess, wantUnchanged := 2, 0
				if mode == ScanModeIncremental {
					wantSuccess, wantUnchanged = 1, 1
				}
				if recovered.Success != wantSuccess || recovered.UnchangedGroups != wantUnchanged || recovered.Added != 0 || recovered.Updated != wantSuccess {
					t.Fatalf("unexpected recovery accounting: %+v", recovered)
				}
				recoveredID, err := s.MovieIDByPath(source)
				if err != nil || recoveredID != id {
					t.Fatalf("replacement recovery changed ID: got=%d want=%d err=%v", recoveredID, id, err)
				}
				movie, err := s.Movie(id)
				if err != nil || movie.Title != "Recovered replacement" || !reflect.DeepEqual(movie.AdditionalParts, before.AdditionalParts) || !reflect.DeepEqual(movie.AutoMarks, before.AutoMarks) {
					t.Fatalf("replacement recovery lost metadata/parts/marks: %+v err=%v", movie, err)
				}
				userAfter, err = s.DataUser(1, id)
				if err != nil || !reflect.DeepEqual(userAfter, userBefore) {
					t.Fatalf("replacement recovery changed personal state: before=%+v after=%+v err=%v", userBefore, userAfter, err)
				}
				actorsAfter, err = s.Actors(id)
				if err != nil || !reflect.DeepEqual(actorsAfter, actors) {
					t.Fatalf("replacement recovery changed actors: %v", err)
				}
				if _, err := s.MovieIDByPath(orphan); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("replacement recovery retained orphan: %v", err)
				}
			})
		}
	}

	t.Run("missing-nfo-snapshot-recheck", func(t *testing.T) {
		root, _, lib := scanFixture(t)
		source := filepath.Join(root, "A.strm")
		meta := filepath.Join(root, "A.nfo")
		if err := os.Remove(meta); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := readGroupInputs(lib, source, []candidate{{path: source, info: info}})
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(meta, 0600); err != nil {
			t.Fatal(err)
		}
		if err := inputs.stillMatches(); !errors.Is(err, regularfile.ErrNotRegular) {
			t.Fatalf("missing NFO replacement lost typed error: %v", err)
		}
	})

	for _, mode := range []string{"full", "incremental", "rescan", "reindex"} {
		t.Run(mode+"/regular-target-symlink", func(t *testing.T) {
			root, s, lib := scanFixture(t)
			source := filepath.Join(root, "A.strm")
			meta := filepath.Join(root, "A.nfo")
			targets := t.TempDir()
			scanWrite(t, filepath.Join(targets, "source"), "https://media.test/symlink.mp4\n")
			scanWrite(t, filepath.Join(targets, "meta"), "<movie><title>Symlink target</title></movie>")
			for path, target := range map[string]string{source: filepath.Join(targets, "source"), meta: filepath.Join(targets, "meta")} {
				if filepath.Dir(path) != root {
					t.Fatal("unexpected symlink replacement path")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			var result Result
			var err error
			switch mode {
			case "full", "incremental":
				result, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanMode(mode)}, nil)
			case "rescan":
				result, err = RescanOne(s, lib, source)
			case "reindex":
				result, err = ReindexOne(s, lib, source)
			}
			id, readErr := s.MovieIDByPath(source)
			if err != nil || readErr != nil || result.Failed != 0 || result.Incomplete || result.Success != 1 {
				t.Fatalf("regular symlink rejected: result=%+v err=%v read=%v", result, err, readErr)
			}
			movie, err := s.Movie(id)
			if err != nil || movie.Title != "Symlink target" {
				t.Fatalf("symlink metadata not indexed: %+v %v", movie, err)
			}
		})
	}
}
