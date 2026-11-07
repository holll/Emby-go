package store

import (
	"path/filepath"
	"testing"
)

func TestTaskRecordsPersistPruneAndInterrupt(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-11-07T00:00:00Z"
	for i := int64(1); i <= 3; i++ {
		if err := s.UpsertTaskRecord(TaskRecord{ID: i, Type: "scan", Status: "queued", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertTaskRecord(TaskRecord{ID: 2, Type: "scan", Status: "succeeded", CreatedAt: now, FinishedAt: now, ResultJSON: `{"added":1}`}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := s.TaskRecordByID(2)
	if err != nil || !ok {
		t.Fatalf("record 2: ok=%v err=%v", ok, err)
	}
	if rec.Status != "succeeded" || rec.ResultJSON != `{"added":1}` {
		t.Fatalf("unexpected record: %+v", rec)
	}
	// 只有 queued/running 会被标成中断，已完成行保持原状态。
	n, err := s.MarkInterruptedTaskRecords(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("interrupted %d, want 2", n)
	}
	rec, _, _ = s.TaskRecordByID(1)
	if rec.Status != "interrupted" || rec.Error == "" {
		t.Fatalf("record 1 not interrupted: %+v", rec)
	}
	if id, err := s.MaxTaskRecordID(); err != nil || id != 3 {
		t.Fatalf("max id = %d, %v", id, err)
	}
	list, err := s.ListTaskRecords(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != 3 || list[2].ID != 1 {
		t.Fatalf("unexpected list order: %+v", list)
	}
	if err := s.PruneTaskRecords(2); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListTaskRecords(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != 3 || list[1].ID != 2 {
		t.Fatalf("prune kept wrong rows: %+v", list)
	}
	if err := s.SetTaskRecordStarted(3, "2026-11-07T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	rec, _, _ = s.TaskRecordByID(3)
	if rec.Status != "running" || rec.StartedAt != "2026-11-07T00:01:00Z" {
		t.Fatalf("start not recorded: %+v", rec)
	}
}
