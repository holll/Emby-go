package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// casOrder records the phases observed through casTxBeginHook: the two-phase
// split exists to keep every filesystem read out of the write-transaction
// window, where writeMu and the SQLite write lock are both pinned (P1-4).
type casOrder struct {
	events []string
}

func (o *casOrder) add(name string) { o.events = append(o.events, name) }

// tryLockProbe reports whether writeMu is currently free. TryLock never blocks,
// so it is safe to call from inside the write-transaction callback of the same
// goroutine that owns the mutex (it simply returns false).
func tryLockProbe(s *Store) bool {
	if !s.writeMu.TryLock() {
		return false
	}
	s.writeMu.Unlock()
	return true
}

// TestArtworkPrepareRunsOutsideWriterLock proves the shape of the poster fix:
// Prepare -- the only place allowed to touch file contents -- runs with writeMu
// free, while the transaction callback and Commit run with it held.
func TestArtworkPrepareRunsOutsideWriterLock(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(m.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	order := &casOrder{}
	prepareFree, txHeld, commitHeld := false, false, false
	defer func() { casTxBeginHook = nil }()
	casTxBeginHook = func() {
		order.add("tx")
		txHeld = !tryLockProbe(f.s)
	}
	err = f.s.CompareAndSwapArtworkFiles(context.Background(), m, lib.Path, "/new/poster.jpg", ArtworkFiles{
		Prepare: func() error {
			order.add("prepare")
			prepareFree = tryLockProbe(f.s)
			return nil
		},
		Commit: func() error {
			order.add("commit")
			commitHeld = !tryLockProbe(f.s)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepareFree {
		t.Fatal("Prepare ran while writeMu was held")
	}
	if !txHeld || !commitHeld {
		t.Fatalf("tx hold=%v commit hold=%v", txHeld, commitHeld)
	}
	if got := strings.Join(order.events, ","); got != "prepare,tx,commit" {
		t.Fatalf("phase order %q", got)
	}
	after, err := f.s.Movie(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PosterPath != "/new/poster.jpg" {
		t.Fatalf("poster %q", after.PosterPath)
	}
}

// TestSpritePrepareRunsOutsideWriterLock is the sprite equivalent: the sheet and
// BIF validation moved into Prepare, so it must not observe the writer lock.
func TestSpritePrepareRunsOutsideWriterLock(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(m.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	order := &casOrder{}
	prepareFree, txHeld := false, false
	defer func() { casTxBeginHook = nil }()
	casTxBeginHook = func() {
		order.add("tx")
		txHeld = !tryLockProbe(f.s)
	}
	err = f.s.CompareAndSwapSpriteFiles(context.Background(), m, lib.Path, SpriteState{}, next, SpriteFiles{
		Prepare: func() error {
			order.add("prepare")
			prepareFree = tryLockProbe(f.s)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepareFree {
		t.Fatal("Prepare ran while writeMu was held")
	}
	if !txHeld {
		t.Fatal("transaction callback did not hold writeMu")
	}
	if got := strings.Join(order.events, ","); got != "prepare,tx" {
		t.Fatalf("phase order %q", got)
	}
	if state, err := f.s.Sprite(m.ID); err != nil || state != next {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

// TestPrepareFailureNeverEntersTransaction pins the cancel/abort path: a failing
// Prepare must return before the writer lock and before any row changes.
func TestPrepareFailureNeverEntersTransaction(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(m.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	next := SpriteState{Revision: 1, Generation: strings.Repeat("a", 32), LibraryID: m.LibraryID, LibraryRoot: lib.Path, SourcePath: m.SourcePath, SourceHash: strings.Repeat("b", 64)}
	before := indexSnapshot(t, f.s)
	boom := errors.New("prepare failed")
	defer func() { casTxBeginHook = nil }()
	casTxBeginHook = func() { t.Error("transaction entered after failed Prepare") }

	if err = f.s.CompareAndSwapArtworkFiles(context.Background(), m, lib.Path, "/new/poster.jpg", ArtworkFiles{Prepare: func() error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("artwork err=%v", err)
	}
	if err = f.s.CompareAndSwapSpriteFiles(context.Background(), m, lib.Path, SpriteState{}, next, SpriteFiles{Prepare: func() error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("sprite err=%v", err)
	}
	if indexSnapshot(t, f.s) != before {
		t.Fatal("failed Prepare changed database")
	}
}
