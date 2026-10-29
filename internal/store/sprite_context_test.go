package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSpriteCancelledConnectionPoolWait(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, _ := f.s.Movie(f.victim)
	lib, _ := f.s.Library(m.LibraryID)
	next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	f.s.db.SetMaxOpenConns(1)
	conn, err := f.s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, operation := range []string{"source", "state", "CAS"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			start := time.Now()
			var err error
			switch operation {
			case "source":
				_, _, err = f.s.SpriteSource(ctx, m.ID)
			case "state":
				_, err = f.s.SpriteContext(ctx, m.ID)
			case "CAS":
				err = f.s.CompareAndSwapSprite(ctx, m, lib.Path, SpriteState{}, next, func() error { t.Error("callback ran despite blocked pool"); return nil })
			}
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("%v after %v", err, time.Since(start))
			}
		})
	}
	if !f.s.writeMu.TryLock() {
		t.Fatal("cancelled CAS leaked writer lock")
	}
	f.s.writeMu.Unlock()
}
func TestSpriteCancelAfterVerificationRollsBack(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, _ := f.s.Movie(f.victim)
	lib, _ := f.s.Library(m.LibraryID)
	next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	before := indexSnapshot(t, f.s)
	ctx, cancel := context.WithCancel(context.Background())
	err := f.s.CompareAndSwapSprite(ctx, m, lib.Path, SpriteState{}, next, func() error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if indexSnapshot(t, f.s) != before {
		t.Fatal("cancelled CAS published pointer or changed data")
	}
}
func TestSpriteIndependentDatabaseWritersCAS(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, _ := f.s.Movie(f.victim)
	lib, _ := f.s.Library(m.LibraryID)
	var seq int
	var name, path string
	if err := f.s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	otherDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(2000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := &Store{db: otherDB}
	first := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	second := first
	second.Generation = strings.Repeat("c", 32)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, s := range []*Store{f.s, other} {
		go func(index int, s *Store) {
			<-start
			next := first
			if index == 1 {
				next = second
			}
			results <- s.CompareAndSwapSprite(context.Background(), m, lib.Path, SpriteState{}, next, nil)
		}(i, s)
	}
	close(start)
	success, rejected := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrSpriteChanged) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
	state, err := f.s.Sprite(m.ID)
	if err != nil || state.Revision != 1 {
		t.Fatalf("%+v %v", state, err)
	}
}
