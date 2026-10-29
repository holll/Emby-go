package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestScheduledSpriteTargetScopeAndSnapshot(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	ctx := context.Background()
	for _, source := range []string{"/fixture/delete/local.mp4", "/fixture/delete/upper.STRM", "/fixture/delete/trailing.strm.bak"} {
		if _, err := f.s.IndexMovie(Movie{LibraryID: f.lib, SourcePath: source, Title: source}, 1, time.Now(), nil); err != nil {
			t.Fatal(err)
		}
	}
	total, maxID, err := f.s.ScheduledSpriteScope(ctx, f.lib)
	if err != nil || total != 2 {
		t.Fatalf("scope count %d max %d %v", total, maxID, err)
	}
	late, err := f.s.IndexMovie(Movie{LibraryID: f.lib, SourcePath: "/fixture/delete/late.strm", Title: "late"}, 1, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	after := int64(0)
	for {
		page, err := f.s.ScheduledSpriteTargets(ctx, f.lib, after, maxID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		target := page[0]
		if target.LibraryID != f.lib || target.ID <= after || ids[target.ID] || target.ID == late {
			t.Fatalf("invalid page %+v", page)
		}
		ids[target.ID] = true
		after = target.ID
	}
	if len(ids) != 2 {
		t.Fatalf("snapshot ids %v", ids)
	}
	if _, err = f.s.db.Exec("UPDATE libraries SET enabled=0 WHERE id=?", f.lib); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.ScheduledSpriteScope(ctx, f.lib); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled %v", err)
	}
	page, err := f.s.ScheduledSpriteTargets(ctx, 0, 0, late, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range page {
		if target.LibraryID == f.lib {
			t.Fatalf("disabled target %+v", target)
		}
	}
	for _, limit := range []int{-1, 0, 201} {
		if _, err = f.s.ScheduledSpriteTargets(ctx, 0, 0, late, limit); err == nil {
			t.Fatalf("limit %d", limit)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = f.s.ScheduledSpriteScope(canceled, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("scope cancel %v", err)
	}
	if _, err = f.s.ScheduledSpriteTargets(canceled, 0, 0, late, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("page cancel %v", err)
	}
}
