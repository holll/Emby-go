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
	_, staleMTime := SourceStat(stale.SourcePath)
	if _, err := s.UpsertMovie(stale, 0, staleMTime); err != nil {
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

// TestRescanOne 单文件重扫：只刷新目标文件，且**不得**触发 DeleteMissingSources
// （否则一次误传路径就会把整库索引删掉）。
func TestRescanOne(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.strm", "http://media.test/a.mp4\n")
	write("a.nfo", "<movie><title>A</title></movie>\n")
	write("b.strm", "http://media.test/b.mp4\n")
	write("b.nfo", "<movie><title>B</title></movie>\n")

	s := newStore(t, root)
	lib, err := s.AddLibrary("t", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if got := len(visible(t, s)); got != 2 {
		t.Fatalf("初始影片数 = %d，期望 2", got)
	}

	// 改写 a 的 NFO 后单文件重扫：只有 a 更新，b 仍在库里。
	write("a.nfo", "<movie><title>A 已更新</title><year>2024</year></movie>\n")
	if _, err := RescanOne(s, lib, filepath.Join(root, "a.strm")); err != nil {
		t.Fatalf("RescanOne: %v", err)
	}
	movies := visible(t, s)
	if len(movies) != 2 {
		t.Fatalf("单文件重扫后影片数 = %d，期望 2（不得删掉其它索引）", len(movies))
	}
	titles := map[string]bool{}
	for _, m := range movies {
		titles[m.Title] = true
	}
	if !titles["A 已更新"] || !titles["B"] {
		t.Errorf("重扫结果不对: %v", titles)
	}

	// 磁盘上删掉 b 后单文件重扫 a：b 的索引必须原样保留（RescanOne 不清理失效行）。
	os.Remove(filepath.Join(root, "b.strm"))
	if _, err := RescanOne(s, lib, filepath.Join(root, "a.strm")); err != nil {
		t.Fatal(err)
	}
	if got := len(visible(t, s)); got != 2 {
		t.Errorf("单文件重扫后影片数 = %d，期望 2（不带 DeleteMissingSources）", got)
	}
	// 而整库 Scan 才会清掉 b。
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	if got := len(visible(t, s)); got != 1 {
		t.Errorf("整库扫描后影片数 = %d，期望 1", got)
	}
}

// TestRescanOneKeepsCDGroup 单文件重扫分集影片时，AdditionalParts 必须按磁盘现状重建。
func TestRescanOneKeepsCDGroup(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Movie-CD1.strm", "Movie-CD2.strm", "Movie-CD3.strm"} {
		write(name, "http://media.test/"+name+".mp4\n")
	}
	write("Movie.nfo", "<movie><title>Split</title></movie>\n")

	s := newStore(t, root)
	lib, err := s.AddLibrary("t", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	movies := visible(t, s)
	if len(movies) != 1 || len(movies[0].AdditionalParts) != 2 {
		t.Fatalf("初始分集结构不对: %+v", movies)
	}

	// 删掉 CD3 后重扫 CD2（分组内任一文件都要能重建整组）。
	if err := os.Remove(filepath.Join(root, "Movie-CD3.strm")); err != nil {
		t.Fatal(err)
	}
	if _, err := RescanOne(s, lib, filepath.Join(root, "Movie-CD2.strm")); err != nil {
		t.Fatalf("RescanOne: %v", err)
	}
	movies = visible(t, s)
	if len(movies) != 1 {
		t.Fatalf("分集影片数 = %d，期望 1", len(movies))
	}
	if got := len(movies[0].AdditionalParts); got != 1 {
		t.Errorf("AdditionalParts 数量 = %d，期望 1: %v", got, movies[0].AdditionalParts)
	}
}

// TestRescanOneRejectsBadPath 非 .strm / 不存在的路径直接报错。
func TestRescanOneRejectsBadPath(t *testing.T) {
	root := t.TempDir()
	s := newStore(t, root)
	lib, err := s.AddLibrary("t", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RescanOne(s, lib, filepath.Join(root, "nope.strm")); err == nil {
		t.Error("不存在的文件应报错")
	}
	if _, err := RescanOne(s, lib, root); err == nil {
		t.Error("目录应报错")
	}
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := RescanOne(s, lib, filepath.Join(root, "x.txt")); err == nil {
		t.Error("非 .strm 文件应报错")
	}
}
