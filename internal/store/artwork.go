package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

var ErrArtworkChanged = errors.New("artwork source or index changed; retry")

// casTxBeginHook, when set by a test, runs as the first statement of the
// poster/sprite write-transaction callback: the writer mutex and the pinned
// SQLite write transaction are both held at that instant. Tests use it to prove
// that the Prepare phase -- and therefore every filesystem read -- ran outside
// this window.
var casTxBeginHook func()

// ArtworkFiles is the filesystem half of CompareAndSwapArtworkFiles, split in
// two phases so no disk read ever happens while the writer mutex and the SQLite
// write transaction are pinned:
//
//   - Prepare runs before the writer lock is taken. Every filesystem read --
//     symlink walks, fstat, fingerprint hashing, decoding -- belongs here, so
//     the possibly multi-megabyte work can never queue UpsertMovie,
//     RecordPlaybackEvent or scan commits behind it.
//   - Commit runs inside the write transaction and may only perform
//     constant-time metadata operations (os.Rename/os.Link). A read there pins
//     both writeMu and the SQLite write lock for the whole read, which is
//     exactly the P1-4 defect ("database is locked" once busy_timeout expires).
//
// Neither callback may call Store methods: writeMu is not reentrant (TryLock),
// so a callback that touches the Store deadlocks on itself.
type ArtworkFiles struct {
	Prepare func() error
	Commit  func() error
}

// CompareAndSwapArtworkFiles serializes the final filesystem publish with index
// writers. files.Prepare checks cancellation and disk state before the publish
// is attempted; files.Commit performs the constant-time publish inside the write
// transaction. A file published by Commit before a failing transaction stays on
// disk and is recoverable by a later run. Only poster_path changes; metadata,
// status, userdata and timestamps stay intact.
func (s *Store) CompareAndSwapArtworkFiles(ctx context.Context, expected Movie, libraryRoot, posterPath string, files ArtworkFiles) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Every filesystem read runs here, before writeMu is requested. Prepare may
	// report ErrArtworkChanged; the transaction below still revalidates the index
	// rows, so a stale expected movie is rejected either way.
	if files.Prepare != nil {
		if err := files.Prepare(); err != nil {
			return err
		}
	}
	var version int64
	// A long-running scan may hold the writer gate. Do not leave a cancelled
	// artwork job waiting indefinitely for it, or spawn a goroutine that later
	// acquires an abandoned lock. LockContext parks in the gate's FIFO queue and
	// returns on ctx.Done (P2-13); the token is never leaked on error.
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	err := s.writeTxOnce(func(tx *sql.Tx) error {
		if casTxBeginHook != nil {
			casTxBeginHook()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query("SELECT "+movieCols+" FROM movies WHERE id=?", expected.ID)
		if err != nil {
			return err
		}
		if !rows.Next() {
			rows.Close()
			return ErrArtworkChanged
		}
		current, err := movieScan(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, expected) {
			return ErrArtworkChanged
		}
		var root string
		if err := tx.QueryRow("SELECT path FROM libraries WHERE id=?", expected.LibraryID).Scan(&root); err != nil {
			return err
		}
		if root != libraryRoot {
			return ErrArtworkChanged
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if files.Commit != nil {
			// Constant-time publish only: os.Link/os.Rename of an already
			// validated temporary file. Anything that reads file content here
			// holds writeMu and the SQLite write lock for the whole read.
			if err := files.Commit(); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("UPDATE movies SET poster_path=? WHERE id=?", posterPath, expected.ID); err != nil {
			return err
		}
		if err := InvalidateScanGroupsForPathsTx(tx, expected.LibraryID, []string{expected.SourcePath}); err != nil {
			return err
		}
		return bumpLibraryVersionTx(tx, expected.LibraryID)
	})
	if err == nil {
		s.publishVersion(version)
	}
	return err
}

// CompareAndSwapArtwork is the single-phase form, kept for callers that need no
// Prepare phase. install runs inside the write transaction, so it must stay
// constant-time (os.Rename/os.Link); filesystem reads belong in
// CompareAndSwapArtworkFiles' Prepare phase.
func (s *Store) CompareAndSwapArtwork(ctx context.Context, expected Movie, libraryRoot, posterPath string, install func() error) error {
	return s.CompareAndSwapArtworkFiles(ctx, expected, libraryRoot, posterPath, ArtworkFiles{Commit: install})
}
