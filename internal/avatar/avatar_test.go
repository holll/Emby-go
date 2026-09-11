package avatar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileNameStableAndNormalized(t *testing.T) {
	base := FileName("三上悠亜")
	if !strings.HasSuffix(base, Ext) {
		t.Errorf("文件名应以 %s 结尾，得到 %q", Ext, base)
	}
	// 大小写与首尾空白不应产生第二份副本（同名演员重刮必须覆盖同一文件）。
	if got := FileName("  三上悠亜  "); got != base {
		t.Errorf("去空白后文件名应一致: %q vs %q", got, base)
	}
	if got := FileName("Alice"); got != FileName("alice") {
		t.Errorf("大小写不敏感: %q vs %q", got, FileName("alice"))
	}
	if FileName("Alice") == FileName("Bob") {
		t.Error("不同演员应映射到不同文件")
	}
}

func TestPathAndTag(t *testing.T) {
	dir := filepath.Join("base", "avatars")
	if got := Path(dir, "Alice"); got != filepath.Join(dir, FileName("Alice")) {
		t.Errorf("Path = %q", got)
	}
	if Tag(nil) != "" || Tag([]byte{}) != "" {
		t.Error("空内容应返回空标识（调用方据此跳过写库）")
	}
	a, b := Tag([]byte("image-a")), Tag([]byte("image-b"))
	if a == "" || a == b {
		t.Errorf("不同内容应给出不同标识: %q %q", a, b)
	}
	if Tag([]byte("image-a")) != a {
		t.Error("同一内容应给出稳定标识")
	}
}

func TestDefaultDir(t *testing.T) {
	cases := []struct{ db, want string }{
		{filepath.Join("data", "emby.db"), filepath.Join("data", "avatars")},
		{"emby.db", "avatars"},
	}
	for _, tc := range cases {
		if got := DefaultDir(tc.db); got != tc.want {
			t.Errorf("DefaultDir(%q) = %q，期望 %q", tc.db, got, tc.want)
		}
	}
	if info, err := os.Stat(DefaultDir(filepath.Join(t.TempDir(), "x.db"))); err == nil && !info.IsDir() {
		t.Error("默认目录不应是普通文件")
	}
}
