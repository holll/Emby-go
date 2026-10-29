package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestArtworkOnlyPosterAndCAS(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	before, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(before.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	versions := indexedDeleteVersions(t, f)
	called := 0
	if err = f.s.CompareAndSwapArtwork(context.Background(), before, lib.Path, "/new/poster.jpg", func() error { called++; return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := f.s.Movie(f.victim)
	after.PosterPath = before.PosterPath
	if !reflect.DeepEqual(before, after) {
		t.Fatal("non-poster movie data changed")
	}
	if called != 1 {
		t.Fatal(called)
	}
	assertIndexedDeleteVersions(t, f, versions, true)
	versions = indexedDeleteVersions(t, f)
	if err = f.s.CompareAndSwapArtwork(context.Background(), before, lib.Path, "/other", func() error { t.Fatal("stale CAS installed image"); return nil }); !errors.Is(err, ErrArtworkChanged) {
		t.Fatal(err)
	}
	assertIndexedDeleteVersions(t, f, versions, false)
}
func TestArtworkRollbackAndSingleInstall(t *testing.T) {
	for _, fault := range []string{"global", "library", "update", "commit", "callback", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			m, _ := f.s.Movie(f.victim)
			lib, _ := f.s.Library(m.LibraryID)
			var ddl string
			switch fault {
			case "global":
				ddl = "CREATE TRIGGER fail_art BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'fail'); END"
			case "library":
				ddl = fmt.Sprintf("CREATE TRIGGER fail_art BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'fail'); END", f.lib)
			case "update":
				ddl = "CREATE TRIGGER fail_art BEFORE UPDATE ON movies BEGIN SELECT RAISE(ABORT,'fail'); END"
			case "commit":
				ddl = "CREATE TABLE art_parent(id INTEGER PRIMARY KEY); CREATE TABLE art_child(id INTEGER REFERENCES art_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_art AFTER UPDATE ON movies BEGIN INSERT INTO art_child VALUES(999); END"
			}
			if ddl != "" {
				if _, e := f.s.db.Exec(ddl); e != nil {
					t.Fatal(e)
				}
			}
			snapshot := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "cancel" {
				cancel()
			}
			calls := 0
			err := f.s.CompareAndSwapArtwork(ctx, m, lib.Path, "new", func() error {
				calls++
				if fault == "callback" {
					return errors.New("fail callback")
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if calls > 1 || (fault == "cancel" && calls != 0) {
				t.Fatal(calls)
			}
			if indexSnapshot(t, f.s) != snapshot {
				t.Fatal("failed transaction modified data")
			}
			assertIndexedDeleteVersions(t, f, versions, false)
		})
	}
}
func TestArtworkCancelledWhileWriterBusy(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(m.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.s.writeMu.Lock()
	locked := true
	defer func() {
		if locked {
			f.s.writeMu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- f.s.CompareAndSwapArtwork(ctx, m, lib.Path, "new", func() error {
			t.Error("cancelled artwork installed image")
			return nil
		})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		f.s.writeMu.Unlock()
		locked = false
		<-done
		t.Fatal("cancelled artwork waited for writer mutex")
	}
}

func TestArtworkLibraryMoveAndCancellationAfterInstall(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, _ := f.s.Movie(f.victim)
	lib, _ := f.s.Library(m.LibraryID)
	if err := f.s.CompareAndSwapArtwork(context.Background(), m, lib.Path+"-changed", "new", func() error { t.Fatal("wrong root installed"); return nil }); !errors.Is(err, ErrArtworkChanged) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.s.CompareAndSwapArtwork(ctx, m, lib.Path, "new", func() error { cancel(); return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := f.s.Movie(m.ID)
	if after.PosterPath != "new" {
		t.Fatal("cancel after installation prevented index")
	}
}
