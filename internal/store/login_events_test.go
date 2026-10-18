package store

import (
	"testing"
	"time"
)

// TestLoginEventRetentionPrunesOldRows 验证 90 天前的登录审计会在写入时清理。
func TestLoginEventRetentionPrunesOldRows(t *testing.T) {
	s, err := Open(t.TempDir() + "/login.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now()
	if err := s.RecordLoginEvent(1, "admin", "127.0.0.1", true, now.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLoginEvent(1, "admin", "127.0.0.1", true, now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM login_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("期望仅保留 1 条，实际 %d", n)
	}
}
