package artwork

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"emby-go/internal/store"
)

// blocker holds the store's writer lock from inside a write transaction, the way
// the pre-fix poster/sprite CAS held it while hashing megabytes of file content.
// It owns a different movie of the same library so the CAS under test still
// matches its own expected row.
type blocker struct {
	locked  chan struct{}
	release chan struct{}
	once    sync.Once
	done    chan error
}

func startBlocker(t *testing.T, db *store.Store, movie store.Movie, root string) *blocker {
	t.Helper()
	b := &blocker{locked: make(chan struct{}), release: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		err := db.CompareAndSwapArtwork(context.Background(), movie, root, "/blocked.jpg", func() error {
			close(b.locked)
			<-b.release
			return nil
		})
		b.done <- err
	}()
	select {
	case <-b.locked:
	case err := <-b.done:
		t.Fatalf("blocker exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("blocker never entered the write transaction")
	}
	return b
}

// stop is idempotent: the test calls it once to release the lock mid-run and
// again from its defer, and only the first call may drain done.
func (b *blocker) stop(t *testing.T) {
	t.Helper()
	b.once.Do(func() {
		close(b.release)
		if err := <-b.done; err != nil {
			t.Errorf("blocker CAS: %v", err)
		}
	})
}

func siblingMovie(t *testing.T, db *store.Store, libraryID int64, dir, name string) store.Movie {
	t.Helper()
	src := filepath.Join(dir, name)
	if err := os.WriteFile(src, []byte("https://blocker.example/video"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := db.UpsertMovie(store.Movie{LibraryID: libraryID, SourcePath: src, Status: "pending", Title: "blocker"}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestInstallReadsFinishBeforeWriterLock proves the P1-4 fix end to end: while a
// writer transaction owns writeMu and the SQLite write lock, a poster Run still
// performs every filesystem read (root checks, source fingerprint, dest
// fingerprints) and stops only at the writer lock. Nothing is published before
// the lock is free, so the reads cannot be hiding inside the transaction.
func TestInstallReadsFinishBeforeWriterLock(t *testing.T) {
	db, m, dir := fixture(t)
	other := siblingMovie(t, db, m.LibraryID, dir, "B.strm")
	b := startBlocker(t, db, other, dir)
	defer b.stop(t)

	prepared := make(chan struct{}, 1)
	phaseHook = func(phase string) {
		if phase == "prepare-done" {
			select {
			case prepared <- struct{}{}:
			default:
			}
		}
	}
	defer func() { phaseHook = nil }()

	c := provider(t, func(w http.ResponseWriter, r *http.Request) { w.Write(picture()) })
	type outcome struct {
		res Result
		err error
	}
	runDone := make(chan outcome, 1)
	go func() {
		res, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "source", ProviderID: "42"}, Options{Client: c})
		runDone <- outcome{res, err}
	}()
	select {
	case <-prepared:
	case out := <-runDone:
		t.Fatalf("Run returned before reaching its write lock: %+v %v", out.res, out.err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run never finished its read-only phase")
	}
	// Every read is done (prepare-done is its last statement) yet the publish
	// must still be pending: only os.Link remains and it needs writeMu.
	if _, err := os.Lstat(m.SourcePath + ".poster.jpg"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("poster published before the writer lock was free: %v", err)
	}
	select {
	case out := <-runDone:
		t.Fatalf("Run finished while writeMu was held: %+v %v", out.res, out.err)
	default:
	}
	b.stop(t)
	select {
	case out := <-runDone:
		if out.err != nil || !out.res.Changed || out.res.RefreshError != "" {
			t.Fatalf("%+v %v", out.res, out.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not finish after the writer lock was released")
	}
	if _, err := os.Stat(m.SourcePath + ".poster.jpg"); err != nil {
		t.Fatalf("poster not installed: %v", err)
	}
	if after, err := db.Movie(m.ID); err != nil || after.PosterPath != m.SourcePath+".poster.jpg" {
		t.Fatalf("poster path %+v %v", after.PosterPath, err)
	}
}

// TestSpriteValidationFinishesBeforeWriterLock is the sprite twin: reading and
// hashing every sheet plus the BIF index happens outside the writer window.
func TestSpriteValidationFinishesBeforeWriterLock(t *testing.T) {
	db, m, dir := fixture(t)
	other := siblingMovie(t, db, m.LibraryID, dir, "B.strm")
	opts := spriteTestOptions(t, dir, "1")
	b := startBlocker(t, db, other, dir)
	defer b.stop(t)

	prepared := make(chan struct{}, 1)
	phaseHook = func(phase string) {
		if phase == "prepare-done" {
			select {
			case prepared <- struct{}{}:
			default:
			}
		}
	}
	defer func() { phaseHook = nil }()

	type outcome struct {
		res SpriteResult
		err error
	}
	runDone := make(chan outcome, 1)
	go func() {
		res, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID}, opts)
		runDone <- outcome{res, err}
	}()
	select {
	case <-prepared:
	case out := <-runDone:
		t.Fatalf("GenerateSprite returned before reaching its write lock: %+v %v", out.res, out.err)
	case <-time.After(60 * time.Second):
		t.Fatal("GenerateSprite never finished its read-only phase")
	}
	// Sheets and BIF were read while the blocker held the write transaction, yet
	// the pointer must still be unset: the publish is the in-transaction UPDATE.
	// No pointer row exists yet: publication is the in-transaction kv UPDATE.
	if state, err := db.Sprite(m.ID); state.Generation != "" || (err != nil && !errors.Is(err, sql.ErrNoRows)) {
		t.Fatalf("sprite published before the writer lock was free: %+v %v", state, err)
	}
	select {
	case out := <-runDone:
		t.Fatalf("GenerateSprite finished while writeMu was held: %+v %v", out.res, out.err)
	default:
	}
	b.stop(t)
	select {
	case out := <-runDone:
		if out.err != nil || !out.res.Changed || out.res.Manifest.Generation == "" {
			t.Fatalf("%+v %v", out.res, out.err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("GenerateSprite did not finish after the writer lock was released")
	}
	if state, err := db.Sprite(m.ID); err != nil || state.Generation == "" {
		t.Fatalf("sprite not published: %+v %v", state, err)
	}
}
