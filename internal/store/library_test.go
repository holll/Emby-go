package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestMoveLibraryReorders 媒体库顺序调整：相邻交换、两端静默不动、不存在的库返回 NotFound。
// 存量库的 sort_order 全是 0（迁移补列的默认值），所以这里刻意不做任何回填就调 Move。
func TestMoveLibraryReorders(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a, err := s.AddLibrary("A", "/tmp/lib-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddLibrary("B", "/tmp/lib-b")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.AddLibrary("C", "/tmp/lib-c")
	if err != nil {
		t.Fatal(err)
	}

	names := func() []string {
		libs, err := s.Libraries()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(libs))
		for _, l := range libs {
			out = append(out, l.Name)
		}
		return out
	}
	equal := func(want ...string) {
		t.Helper()
		got := names()
		if len(got) != len(want) {
			t.Fatalf("顺序 = %v，期望 %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("顺序 = %v，期望 %v", got, want)
			}
		}
	}

	equal("A", "B", "C")

	if err := s.MoveLibrary(c.ID, true); err != nil {
		t.Fatalf("上移 C: %v", err)
	}
	equal("A", "C", "B")

	if err := s.MoveLibrary(a.ID, false); err != nil {
		t.Fatalf("下移 A: %v", err)
	}
	equal("C", "A", "B")

	// 已在两端：静默无操作且不报错，前端按钮本身会置灰。
	if err := s.MoveLibrary(c.ID, true); err != nil {
		t.Fatalf("已在顶端再上移: %v", err)
	}
	if err := s.MoveLibrary(b.ID, false); err != nil {
		t.Fatalf("已在底端再下移: %v", err)
	}
	equal("C", "A", "B")

	if err := s.MoveLibrary(99999, true); !NotFound(err) {
		t.Fatalf("不存在的库应返回 ErrNoRows，得到 %v", err)
	}
}

// TestLibraryCounts 按库统计影片数量（管理端媒体库页展示用）。
func TestLibraryCounts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	l1, err := s.AddLibrary("L1", "/tmp/c1")
	if err != nil {
		t.Fatal(err)
	}
	l2, err := s.AddLibrary("L2", "/tmp/c2")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.UpsertMovie(Movie{
			LibraryID:  l1.ID,
			Title:      name,
			SourcePath: filepath.Join("/tmp/c1", name+".strm"),
			Status:     "success",
		}, 0, time.Time{}); err != nil {
			t.Fatalf("写入 %s: %v", name, err)
		}
	}
	if _, err := s.UpsertMovie(Movie{
		LibraryID:  l2.ID,
		Title:      "x",
		SourcePath: "/tmp/c2/x.strm",
		Status:     "success",
	}, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	counts, err := s.LibraryCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts[l1.ID] != 3 {
		t.Errorf("L1 影片数 = %d，期望 3", counts[l1.ID])
	}
	if counts[l2.ID] != 1 {
		t.Errorf("L2 影片数 = %d，期望 1", counts[l2.ID])
	}
}
