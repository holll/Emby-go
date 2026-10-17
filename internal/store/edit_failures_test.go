package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestEditFailureLifecycle(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "failures.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.db.Exec(`INSERT INTO libraries(id,name,path) VALUES(1,'L','/l');
INSERT INTO movies(id,library_id,source_path,status,title,created_at,updated_at) VALUES(7,1,'/l/a.strm','manual','Reveal','t0','t0')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if err := s.MarkEditFailure(7, "NFO_READ_FAILED", now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEditFailure(7, "INDEX_REFRESH_FAILED", now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	failures, err := s.EditFailures(now)
	if err != nil || len(failures) != 1 {
		t.Fatalf("failures=%v err=%v", failures, err)
	}
	got := failures[0]
	if got.MovieID != 7 || got.Title != "Reveal" || got.Code != "INDEX_REFRESH_FAILED" || got.Attempts != 2 {
		t.Fatalf("merged failure=%+v", got)
	}
	if got.FirstFailedAt != failures[0].FirstFailedAt || got.AgeDays != 8 {
		t.Fatalf("first failed at lost: %+v", got)
	}
	total, stale, err := s.EditFailureCounts(now, 7*24*time.Hour)
	if err != nil || total != 1 || stale != 1 {
		t.Fatalf("counts=%d/%d err=%v", total, stale, err)
	}
	if err := s.ClearEditFailure(7); err != nil {
		t.Fatal(err)
	}
	if failures, err := s.EditFailures(now); err != nil || len(failures) != 0 {
		t.Fatalf("clear left %v err=%v", failures, err)
	}
}
