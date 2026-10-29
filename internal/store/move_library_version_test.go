package store

import (
	"database/sql"
	"errors"
	"testing"
)

// MoveLibrary 之前是裸 s.db.Begin()：不进 writeMu、不推进版本，顺序变了客户端
// 仍拿旧缓存。这里钉住三条：全局/两个库的版本都前进、失败路径不推进、
// 顺序语义与原来的相邻交换一致。
func TestMoveLibraryAdvancesVersions(t *testing.T) {
	s, err := Open(t.TempDir() + "/move.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		lib, err := s.AddLibrary(name, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, lib.ID)
	}
	// AddLibrary 的顺序即 a,b,c（sort_order 递增）
	libs, err := s.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 3 || libs[0].Name != "a" || libs[1].Name != "b" || libs[2].Name != "c" {
		t.Fatalf("初始顺序异常: %+v", libs)
	}

	libVersion := func(id int64) int64 {
		var v int64
		row := s.db.QueryRow("SELECT CAST(value AS INTEGER) FROM kv WHERE key=?", "lib:"+itoa64(id)+":version")
		if err := row.Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0 // 从未 bump 过的库没有对应行
			}
			t.Fatalf("读库版本失败: %v", err)
		}
		return v
	}

	before := s.gversion.Load()
	vA, vB := libVersion(ids[0]), libVersion(ids[1])
	// 把 c 上移 → 与 b 交换
	if err := s.MoveLibrary(ids[2], true); err != nil {
		t.Fatal(err)
	}
	if got := s.gversion.Load(); got <= before {
		t.Fatalf("全局版本未推进: %d → %d", before, got)
	}
	if libVersion(ids[2]) <= vB {
		t.Fatalf("被移动库的版本未推进: %d", libVersion(ids[2]))
	}
	if libVersion(ids[1]) <= vB {
		t.Fatalf("被交换库的版本未推进: %d", libVersion(ids[1]))
	}
	if vA != libVersion(ids[0]) {
		t.Fatalf("未参与的库版本被推进: %d → %d", vA, libVersion(ids[0]))
	}
	libs, err = s.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	if libs[0].Name != "a" || libs[1].Name != "c" || libs[2].Name != "b" {
		t.Fatalf("交换后顺序异常: %+v", libs)
	}

	// 不存在的库：整体回滚，版本不得前进（首条语句虽已 bump，但事务回滚）。
	gv := s.gversion.Load()
	if err := s.MoveLibrary(999999, true); !NotFound(err) {
		t.Fatalf("不存在库应返回 NotFound，实际 %v", err)
	}
	if got := s.gversion.Load(); got != gv {
		t.Fatalf("失败路径推进了全局版本: %d → %d", gv, got)
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
