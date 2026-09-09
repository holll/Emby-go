package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"emby-go/internal/store"
)

// newStore 在临时目录建库并登记 root 为媒体库。
func newStore(t *testing.T, root string) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func visible(t *testing.T, s *store.Store) []store.Movie {
	t.Helper()
	movies, _, err := s.SearchAll(0, "", "", "title", false, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	return movies
}

// CD1/CD2/CD3 应归为一部逻辑影片（CD2/CD3 作为 AdditionalParts），不是任意段数上限。
func TestScanStacksAllCDParts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Movie-CD1.strm", "Movie-CD2.strm", "Movie-CD3.strm"} {
		os.WriteFile(filepath.Join(root, name), []byte("http://media.test/"+name+".mp4\n"), 0644)
	}
	os.WriteFile(filepath.Join(root, "Movie-CD1.nfo"), []byte(`<movie><title>Split</title></movie>`), 0644)

	s := newStore(t, root)
	lib, err := s.AddLibrary("t", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	movies := visible(t, s)
	if len(movies) != 1 {
		t.Fatalf("CD1/2/3 should be one logical movie, got %d", len(movies))
	}
	if parts := movies[0].AdditionalParts; len(parts) != 2 {
		t.Fatalf("expected CD2+CD3 as additional parts, got %v", parts)
	}
}

// 旧版本把 CD3 当独立影片入库时，重扫应把它清掉（片段不进 movies 表）。
func TestScanPurgesStalePartRows(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Movie-CD1.strm", "Movie-CD2.strm", "Movie-CD3.strm"} {
		os.WriteFile(filepath.Join(root, name), []byte("http://media.test/"+name+".mp4\n"), 0644)
	}
	os.WriteFile(filepath.Join(root, "Movie-CD1.nfo"), []byte(`<movie><title>Split</title></movie>`), 0644)

	s := newStore(t, root)
	lib, err := s.AddLibrary("t", root)
	if err != nil {
		t.Fatal(err)
	}
	stale := store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "Movie-CD3.strm"), OutputDir: root, Status: "success", Title: "stale CD3"}
	if _, err := s.UpsertMovie(stale, 0, MTime(stale.SourcePath)); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if movies := visible(t, s); len(movies) != 1 {
		t.Fatalf("stale CD3 row should be purged, got %d movies", len(movies))
	}
}

// 影片新增/删除/移动后重扫应与磁盘一致。
func TestScanReconcilesAddDeleteMove(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "A")
	os.MkdirAll(src, 0755)
	os.WriteFile(filepath.Join(src, "ABC.strm"), []byte("http://media.test/a.mp4\n"), 0644)
	os.WriteFile(filepath.Join(src, "ABC.nfo"), []byte(`<movie><title>ABC</title></movie>`), 0644)

	s := newStore(t, root)
	lib, err := s.AddLibrary("t", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if len(visible(t, s)) != 1 {
		t.Fatal("first scan should index 1 movie")
	}

	// 移动（同库内换目录）
	dst := filepath.Join(root, "B")
	os.MkdirAll(dst, 0755)
	os.Rename(filepath.Join(src, "ABC.strm"), filepath.Join(dst, "ABC.strm"))
	os.Rename(filepath.Join(src, "ABC.nfo"), filepath.Join(dst, "ABC.nfo"))
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	movies := visible(t, s)
	if len(movies) != 1 || movies[0].SourcePath != filepath.Join(dst, "ABC.strm") {
		t.Fatalf("move should reconcile to one movie at new path: %+v", movies)
	}

	// 删除源文件
	os.Remove(filepath.Join(dst, "ABC.strm"))
	os.Remove(filepath.Join(dst, "ABC.nfo"))
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if movies := visible(t, s); len(movies) != 0 {
		t.Fatalf("deleted source should be purged, got %d", len(movies))
	}
}
