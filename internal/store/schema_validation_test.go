package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// 迁移 2–11 建的表/索引同样纳入校验：缺列、多余列、缺索引都必须拒绝启动。
func TestSchemaDriftInMigratedTablesRejected(t *testing.T) {
	cases := []struct {
		name string
		stmt string
	}{
		{"缺少迁移表列", "ALTER TABLE playback_sessions DROP COLUMN device_id"},
		{"迁移表多余列", "ALTER TABLE users ADD COLUMN junk TEXT"},
		{"缺少迁移索引", "DROP INDEX idx_userdata_movie"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "drift.db")
			s, err := Open(path)
			if err != nil {
				t.Fatalf("初始建库失败: %v", err)
			}
			s.Close()

			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.stmt); err != nil {
				db.Close()
				t.Fatalf("注入漂移失败: %v", err)
			}
			db.Close()

			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatalf("接受了漂移结构: %s", tc.name)
			} else if !strings.Contains(err.Error(), "schema drift") {
				t.Fatalf("错误未命中 schema drift: %v", err)
			}
		})
	}
}
