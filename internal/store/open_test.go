package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenKeepsDBPathAndPragmas 连接串带 pragma 后必须仍然按原路径建库
// （参数不能被当成文件名），且每条连接都要有 foreign_keys（per-connection pragma，
// 只 Exec 一次覆盖不到连接池里新开的连接）。
func TestOpenKeepsDBPathAndPragmas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "check.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("库文件未按原路径创建: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.ContainsAny(entry.Name(), "?&=") {
			t.Fatalf("连接串参数被当成了文件名: %s", entry.Name())
		}
	}

	var journalMode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode = %q，期望 wal", journalMode)
	}
}
