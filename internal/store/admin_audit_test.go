package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAdminAuditRecordsAndPrunes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	if err := s.RecordAdminAudit(1, "admin", "POST", "/api/admin/users", 200, now.Add(-120*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAdminAudit(1, "admin", "PUT", "/api/admin/users/2", 200, now); err != nil {
		t.Fatal(err)
	}
	events, err := s.AdminAuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Method != "PUT" || events[0].Path != "/api/admin/users/2" || events[0].Status != 200 {
		t.Fatalf("events=%+v", events)
	}
	if err := s.RecordAdminAudit(1, "admin", "DELETE", "/api/admin/users/2", 204, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = s.AdminAuditEvents(0)
	if len(events) != 2 || events[0].Method != "DELETE" {
		t.Fatalf("events=%+v", events)
	}
}
