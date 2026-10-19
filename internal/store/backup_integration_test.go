package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/backup"
)

// 生产形态：WAL 库仍被服务进程打开时，CLI 以只读方式快照。
func TestBackupAgainstLiveWALStore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "emby.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec("INSERT INTO libraries(name,path) VALUES('L','/tmp/l')"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	result, err := backup.Run(backup.Options{DBPath: path, OutDir: filepath.Join(root, "backups"), Now: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatalf("在线 WAL 库备份失败: %v", err)
	}
	db, err := sql.Open("sqlite", result.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow("SELECT name FROM libraries").Scan(&name); err != nil || name != "L" {
		t.Fatalf("快照数据 name=%q err=%v", name, err)
	}
}
