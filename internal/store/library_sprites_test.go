package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestLibrarySpriteSettingsIndependentAndBounded(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	ctx := context.Background()
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.AddLibrary("Other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	versions := indexedDeleteVersions(t, f)
	if n, err := f.s.LibrarySpriteInterval(ctx, m.LibraryID); err != nil || n != 10 {
		t.Fatalf("default %d %v", n, err)
	}
	for _, n := range []int{10, 60, 300, 17, 86400} {
		if err := f.s.SetLibrarySpriteInterval(ctx, m.LibraryID, n); err != nil {
			t.Fatal(err)
		}
		got, err := f.s.LibrarySpriteInterval(ctx, m.LibraryID)
		if err != nil || got != n {
			t.Fatalf("got %d %v", got, err)
		}
	}
	for _, n := range []int{-1, 0, 1, 9, 86401} {
		if err := f.s.SetLibrarySpriteInterval(ctx, m.LibraryID, n); err == nil {
			t.Fatalf("accepted %d", n)
		}
	}
	if n, err := f.s.LibrarySpriteInterval(ctx, other.ID); err != nil || n != 10 {
		t.Fatalf("cross-library %d %v", n, err)
	}
	all, err := f.s.LibrarySpriteIntervals(ctx)
	if err != nil || all[m.LibraryID] != 86400 || all[other.ID] != 10 {
		t.Fatalf("map %v %v", all, err)
	}
	assertIndexedDeleteVersions(t, f, versions, false)
	var seq int
	var schema, databasePath string
	if err := f.s.db.QueryRow("PRAGMA database_list").Scan(&seq, &schema, &databasePath); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if n, err := reopened.LibrarySpriteInterval(ctx, m.LibraryID); err != nil || n != 86400 {
		t.Fatalf("persisted %d %v", n, err)
	}
	if err := f.s.SetLibrarySpriteInterval(ctx, 999999, 60); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing library %v", err)
	}
	if _, err = f.s.db.Exec("UPDATE libraries SET enabled=0 WHERE id=?", m.LibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.LibrarySpriteInterval(ctx, m.LibraryID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled read %v", err)
	}
	if err = f.s.SetLibrarySpriteInterval(ctx, m.LibraryID, 60); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled write %v", err)
	}
}
