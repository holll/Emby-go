package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIncrementalRetainsManualIdentityAndUserData(t *testing.T) {
	root, s, lib := scanFixture(t)
	source := filepath.Join(root, "A.strm")
	nfoPath := filepath.Join(root, "A.nfo")
	id, err := s.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIndexedManual(source, nfoPath); err != nil {
		t.Fatal(err)
	}
	before, err := s.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser("viewer", "viewer-password", false)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{1, user.ID}
	snapshots := make(map[int64]any)
	for index, userID := range ids {
		if err := s.SavePlaybackForUser(userID, id, int64(index+1)*100, 3, "2026-10-23T01:00:00Z", -1); err != nil {
			t.Fatal(err)
		}
		data, err := s.DataUser(userID, id)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[userID] = data
	}
	for _, scenario := range []string{"changed-nfo", "missing-nfo", "invalid-source"} {
		switch scenario {
		case "changed-nfo":
			scanWrite(t, nfoPath, "<movie><title>Changed</title></movie>")
		case "missing-nfo":
			if err := os.Remove(nfoPath); err != nil {
				t.Fatal(err)
			}
		case "invalid-source":
			scanWrite(t, source, "ed2k://not-playable")
		}
		if _, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil); err != nil {
			t.Fatal(err)
		}
		movie, err := s.Movie(id)
		if err != nil {
			t.Fatal(err)
		}
		if movie.ID != before.ID || movie.CreatedAt != before.CreatedAt || movie.Status != "manual" {
			t.Fatalf("%s lost manual identity %+v -> %+v", scenario, before, movie)
		}
		for _, userID := range ids {
			data, err := s.DataUser(userID, id)
			if err != nil || !reflect.DeepEqual(data, snapshots[userID]) {
				t.Fatalf("userdata lost %d %+v %v", userID, data, err)
			}
		}
		got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
		if err != nil || got.UnchangedGroups != 1 {
			t.Fatalf("retained manual steady state: %+v %v", got, err)
		}
	}
}

func TestIncrementalNFOSelectionSwitchesAndReadFailure(t *testing.T) {
	root := t.TempDir()
	for _, part := range []string{"Film-CD1", "Film-CD2"} {
		scanWrite(t, filepath.Join(root, part+".strm"), "https://media.test/a.mp4")
	}
	shared := filepath.Join(root, "Film.nfo")
	sidecar := filepath.Join(root, "Film-CD1.nfo")
	scanWrite(t, shared, "<movie><title>Shared</title></movie>")
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", root)
	if err != nil {
		t.Fatal(err)
	}
	check := func(title string) {
		t.Helper()
		got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.MovieIDByPath(filepath.Join(root, "Film-CD1.strm"))
		if err != nil {
			t.Fatal(err)
		}
		movie, err := s.Movie(id)
		if err != nil || movie.Title != title {
			t.Fatalf("selection %+v %v result=%+v", movie, err, got)
		}
	}
	check("Shared")
	scanWrite(t, sidecar, "<movie><title>Legacy</title></movie>")
	check("Legacy")
	scanWrite(t, sidecar, "<movie><title>Broken")
	check("Shared")
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	check("Shared")
	before := s.Version("g:version")
	// A directory is a deterministic read error even when tests run as root.
	if err := os.Mkdir(sidecar, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
	if !errors.Is(err, ErrScanIncomplete) || !got.Incomplete || got.CleanupPerformed || s.Version("g:version") != before {
		t.Fatalf("I/O error accepted: %+v %v", got, err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	check("Shared")
}

func TestIncrementalImagesFollowLegacyPriority(t *testing.T) {
	root, s, lib := scanFixture(t)
	for _, name := range []string{"default.JPG", "cover.png", "folder.jpeg", "poster.jpg", "poster.webp"} {
		scanWrite(t, filepath.Join(root, name), name)
		got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeIncremental}, nil)
		if err != nil || got.Updated != 1 {
			t.Fatalf("image update %s %+v %v", name, got, err)
		}
		id, err := s.MovieIDByPath(filepath.Join(root, "A.strm"))
		if err != nil {
			t.Fatal(err)
		}
		movie, err := s.Movie(id)
		if err != nil || movie.PosterPath != filepath.Join(root, name) {
			t.Fatalf("wrong selected image: %+v %v", movie, err)
		}
	}
}
