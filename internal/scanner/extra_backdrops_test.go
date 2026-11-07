package scanner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"emby-go/internal/store"
)

func TestExtraBackdropSnapshotsCanonicalNumericOrder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "extrafanart")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fanart10.jpg", "fanart2.jpg", "fanart1.jpg", "fanart01.jpg", "fanart0.jpg", "fanart-1.jpg", "fanart3.png", "notes.jpg", "fanart4.JPG"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "fanart5.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "A.strm")
	images, err := extraBackdropSnapshots(root, source)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, image := range images {
		if image.Source != source || image.Kind != "extra_backdrop" || !image.Present || image.Size == 0 {
			t.Fatalf("invalid snapshot: %+v", image)
		}
		paths = append(paths, image.Path)
	}
	want := []string{filepath.Join(dir, "fanart1.jpg"), filepath.Join(dir, "fanart2.jpg"), filepath.Join(dir, "fanart10.jpg")}
	if !slices.Equal(paths, want) {
		t.Fatalf("ordered assets = %v, want %v", paths, want)
	}
	t.Chdir(root)
	images, err = extraBackdropSnapshots(".", source)
	if err != nil {
		t.Fatal(err)
	}
	paths = nil
	for _, image := range images {
		paths = append(paths, image.Path)
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("relative inventory paths = %v, want absolute %v", paths, want)
	}
}

func TestExtraBackdropSnapshotsDoNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "fanart1.jpg"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "extrafanart")); err != nil {
		t.Skip(err)
	}
	images, err := extraBackdropSnapshots(root, "A.strm")
	if err != nil || len(images) != 0 {
		t.Fatalf("linked asset directory followed: %v %v", images, err)
	}
	inside := t.TempDir()
	dir := filepath.Join(inside, "extrafanart")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "fanart1.jpg"), filepath.Join(dir, "fanart1.jpg")); err != nil {
		t.Fatal(err)
	}
	images, err = extraBackdropSnapshots(inside, "A.strm")
	if err != nil || len(images) != 0 {
		t.Fatalf("linked asset file followed: %v %v", images, err)
	}
}

func TestScanExtraBackdropsRebuildAndIncremental(t *testing.T) {
	root, s, lib := scanFixture(t)
	source := filepath.Join(root, "A.strm")
	dir := filepath.Join(root, "extrafanart")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fanart10.jpg", "fanart2.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scan := func() Result {
		t.Helper()
		result, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertPaths := func(storeDB *store.Store, want []string) {
		t.Helper()
		id, err := storeDB.MovieIDByPath(source)
		if err != nil {
			t.Fatal(err)
		}
		movie, err := storeDB.Movie(id)
		if err != nil || !slices.Equal(movie.ExtraBackdrops, want) {
			t.Fatalf("extra backdrops = %v, want %v; %v", movie.ExtraBackdrops, want, err)
		}
	}
	second, tenth := filepath.Join(dir, "fanart2.jpg"), filepath.Join(dir, "fanart10.jpg")
	if result := scan(); result.Updated != 1 || !result.IndexChanged {
		t.Fatalf("added images not indexed: %+v", result)
	}
	assertPaths(s, []string{second, tenth})
	if result := scan(); result.UnchangedGroups != 1 || result.IndexChanged {
		t.Fatalf("stable image list must skip: %+v", result)
	}
	// Different size makes image mutation detectable without sleeps or clock assumptions.
	if err := os.WriteFile(tenth, []byte("changed image payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := scan(); result.Updated != 1 {
		t.Fatalf("image edit not detected: %+v", result)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	scan()
	assertPaths(s, []string{tenth})
	if _, err := RescanOne(s, lib, source); err != nil {
		t.Fatal(err)
	}
	assertPaths(s, []string{tenth})
	// A fresh DB reconstructs order from disk, not an old JSON cache.
	rebuilt := newStore(t, t.TempDir())
	rebuiltLib, err := rebuilt.AddLibrary("rebuilt", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(rebuilt, rebuiltLib); err != nil {
		t.Fatal(err)
	}
	assertPaths(rebuilt, []string{tenth})
	if err := os.Remove(tenth); err != nil {
		t.Fatal(err)
	}
	scan()
	assertPaths(s, nil)
}

func TestScanExtraBackdropsPreservesMetadata(t *testing.T) {
	for _, state := range []string{"manual", "unreadable"} {
		for _, mode := range []string{"full", "incremental", "rescan", "reindex"} {
			t.Run(state+"/"+mode, func(t *testing.T) {
				root, s, lib := scanFixture(t)
				source := filepath.Join(root, "A.strm")
				id, err := s.MovieIDByPath(source)
				if err != nil {
					t.Fatal(err)
				}
				if state == "manual" {
					if err := s.MarkManual(source, filepath.Join(root, "A.nfo")); err != nil {
						t.Fatal(err)
					}
				}
				before, err := s.Movie(id)
				if err != nil {
					t.Fatal(err)
				}
				actors, err := s.Actors(id)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.SavePlaybackForUser(1, id, 123, 3, "2026-11-04T01:00:00Z", -1); err != nil {
					t.Fatal(err)
				}
				userdata, err := s.DataUser(1, id)
				if err != nil {
					t.Fatal(err)
				}
				if state == "manual" {
					if err := os.Remove(filepath.Join(root, "A.nfo")); err != nil {
						t.Fatal(err)
					}
				} else {
					scanWrite(t, filepath.Join(root, "A.nfo"), "<movie>")
				}
				dir := filepath.Join(root, "extrafanart")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				image := filepath.Join(dir, "fanart1.jpg")
				scanWrite(t, image, "image")
				scan := func() {
					t.Helper()
					var err error
					switch mode {
					case "full":
						_, err = Scan(s, lib)
					case "incremental":
						_, err = ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
					case "rescan":
						_, err = RescanOne(s, lib, source)
					case "reindex":
						_, err = ReindexOne(s, lib, source)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, want := range [][]string{{image}, nil} {
					scan()
					after, err := s.Movie(id)
					if err != nil || after.ID != before.ID || after.Title != before.Title || after.Status != before.Status || after.ProviderID != before.ProviderID || !slices.Equal(after.ExtraBackdrops, want) {
						t.Fatalf("metadata/extras changed incorrectly: before=%+v after=%+v want=%v err=%v", before, after, want, err)
					}
					afterActors, err := s.Actors(id)
					if err != nil || !reflect.DeepEqual(afterActors, actors) {
						t.Fatalf("actors lost: %v -> %v err=%v", actors, afterActors, err)
					}
					afterData, err := s.DataUser(1, id)
					if err != nil || !reflect.DeepEqual(afterData, userdata) {
						t.Fatalf("userdata lost: %+v -> %+v err=%v", userdata, afterData, err)
					}
					if len(want) > 0 {
						if err := os.Remove(image); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestExtraBackdropRemovalInvalidatesInFlightSnapshot(t *testing.T) {
	root, _, lib := scanFixture(t)
	source := filepath.Join(root, "A.strm")
	dir := filepath.Join(root, "extrafanart")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "fanart1.jpg"), filepath.Join(dir, "fanart2.jpg")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{first, second} {
		inputs, err := readGroupInputs(lib, source, []candidate{{path: source, info: info}})
		if err != nil {
			t.Fatal(err)
		}
		if err := inputs.stillMatches(); err != nil {
			t.Fatal("unchanged snapshot rejected")
		}
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
		if err := inputs.stillMatches(); err == nil {
			t.Fatal("subset/last-image deletion accepted before scan commit")
		}
	}
}
