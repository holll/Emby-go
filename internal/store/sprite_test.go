package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSpritePointerOnlyAndCAS(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(m.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	before := indexSnapshot(t, f.s)
	versions := indexedDeleteVersions(t, f)
	next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	calls := 0
	if err = f.s.CompareAndSwapSprite(context.Background(), m, lib.Path, SpriteState{}, next, func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.Sprite(m.ID)
	if err != nil || state != next {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	after, _ := f.s.Movie(m.ID)
	if !reflect.DeepEqual(m, after) {
		t.Fatal("movie metadata changed")
	}
	assertIndexedDeleteVersions(t, f, versions, false)
	if err = f.s.CompareAndSwapSprite(context.Background(), m, lib.Path, SpriteState{}, next, func() error { t.Fatal("stale CAS callback ran"); return nil }); !errors.Is(err, ErrSpriteChanged) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if _, err = f.s.db.Exec("DELETE FROM kv WHERE key=?", spriteKey(m.ID)); err != nil {
		t.Fatal(err)
	}
	if indexSnapshot(t, f.s) != before {
		t.Fatal("non-sprite database state changed")
	}
}
func TestSpriteRollbackCancellationAndMetadataCAS(t *testing.T) {
	for _, fault := range []string{"verify", "cancel", "movie", "root", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			m, _ := f.s.Movie(f.victim)
			lib, _ := f.s.Library(m.LibraryID)
			next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
			if fault == "movie" {
				m.Title = "stale"
			}
			if fault == "root" {
				lib.Path += "-stale"
				next.LibraryRoot = lib.Path
			}
			if fault == "commit" {
				ddl := fmt.Sprintf("CREATE TABLE spr_parent(id INTEGER PRIMARY KEY);CREATE TABLE spr_child(id INTEGER REFERENCES spr_parent(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER fail_spr AFTER UPDATE ON kv WHEN NEW.key='%s' BEGIN INSERT INTO spr_child VALUES(999); END", spriteKey(m.ID))
				if _, err := f.s.db.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			before := indexSnapshot(t, f.s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "cancel" {
				cancel()
			}
			calls := 0
			err := f.s.CompareAndSwapSprite(ctx, m, lib.Path, SpriteState{}, next, func() error {
				calls++
				if fault == "verify" {
					return errors.New("changed source")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected failure")
			}
			if (fault == "movie" || fault == "root" || fault == "cancel") && calls != 0 {
				t.Fatal("callback ran for rejected CAS")
			}
			if indexSnapshot(t, f.s) != before {
				t.Fatal("rollback changed database")
			}
		})
	}
}
func TestSpriteCancelledWriterWait(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, _ := f.s.Movie(f.victim)
	lib, _ := f.s.Library(m.LibraryID)
	next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	f.s.writeMu.Lock()
	defer f.s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := f.s.CompareAndSwapSprite(ctx, m, lib.Path, SpriteState{}, next, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}
