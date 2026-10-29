package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// SpriteState is a separate preview pointer. It never changes movie metadata or
// the scan/global/library index versions. The generation's files are immutable.
type SpriteState struct {
	Revision    int64  `json:"revision"`
	Generation  string `json:"generation"`
	LibraryID   int64  `json:"library_id"`
	SourcePath  string `json:"source_path"`
	LibraryRoot string `json:"library_root"`
	SourceHash  string `json:"source_hash"`
}

var ErrSpriteChanged = errors.New("sprite source or generation changed; retry")

func spriteKey(movieID int64) string { return fmt.Sprintf("sprite:%d", movieID) }

func (s *Store) Sprite(movieID int64) (SpriteState, error) {
	return s.SpriteContext(context.Background(), movieID)
}

func (s *Store) SpriteContext(ctx context.Context, movieID int64) (SpriteState, error) {
	var raw string
	var state SpriteState
	if movieID <= 0 {
		return state, errors.New("invalid movie ID")
	}
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE key=?", spriteKey(movieID)).Scan(&raw); err != nil {
		return state, err
	}
	if len(raw) > 8192 {
		return state, errors.New("invalid sprite state size")
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return state, err
	}
	if state.Revision <= 0 || state.Generation == "" {
		return state, errors.New("invalid sprite state")
	}
	return state, nil
}

// SpriteSource is a context-aware source snapshot for preview requests. The
// final publication rechecks both rows inside the reserved write transaction.
func (s *Store) SpriteSource(ctx context.Context, id int64) (Movie, string, error) {
	var movie Movie
	if id <= 0 {
		return movie, "", errors.New("invalid movie ID")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+movieCols+" FROM movies WHERE id=?", id)
	if err != nil {
		return movie, "", err
	}
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err == nil {
			err = sql.ErrNoRows
		}
		return movie, "", err
	}
	movie, err = movieScan(rows)
	rows.Close()
	if err != nil {
		return movie, "", err
	}
	var root string
	err = s.db.QueryRowContext(ctx, "SELECT path FROM libraries WHERE id=? AND enabled=1", movie.LibraryID).Scan(&root)
	return movie, root, err
}

// spriteWriteTx has the same failed-COMMIT connection hygiene as writeTxOnce,
// but unlike that legacy helper it also bounds pool wait and BEGIN by ctx.
func (s *Store) spriteWriteTx(ctx context.Context, write func(*sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			discardWriteConn(conn)
		}
	}()
	if err = write(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			discardWriteConn(conn)
		}
		return err
	}
	return nil
}

// SpriteFiles is the filesystem half of CompareAndSwapSpriteFiles, split in two
// phases for the same reason as ArtworkFiles: a sprite generation is validated
// by reading and hashing every sheet plus the BIF index (tens of megabytes), and
// doing that inside the write transaction pins writeMu and the SQLite write lock
// for the whole read.
//
// Prepare runs before the writer lock is taken and owns every filesystem read
// (source fingerprint, generation identity, sheet/BIF validation). Commit runs
// inside the write transaction and may only perform constant-time metadata
// operations; publication of a sprite generation is the kv UPDATE the store
// performs itself, so commit is normally nil. Neither callback may call Store
// methods: writeMu is not reentrant (TryLock), so a callback that touches the
// Store deadlocks on itself.
type SpriteFiles struct {
	Prepare func() error
	Commit  func() error
}

// CompareAndSwapSpriteFiles commits only the independent preview pointer. Taking
// a real SQLite write lock before reading the movie also excludes other
// processes, not just writers using this Store's mutex. files.Prepare validates
// the generation on disk outside that window; the callback may not call Store
// methods.
func (s *Store) CompareAndSwapSpriteFiles(ctx context.Context, expected Movie, libraryRoot string, old, next SpriteState, files SpriteFiles) error {
	if expected.ID <= 0 || old.Revision < 0 || old.Revision == int64(^uint64(0)>>1) || next.Revision != old.Revision+1 || next.Generation == "" || next.LibraryID != expected.LibraryID || next.SourcePath != expected.SourcePath || next.LibraryRoot != libraryRoot {
		return errors.New("invalid sprite CAS")
	}
	payload, err := json.Marshal(next)
	if err != nil || len(payload) > 8192 {
		return errors.New("invalid sprite state size")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Every filesystem read runs here, before writeMu is requested.
	if files.Prepare != nil {
		if err := files.Prepare(); err != nil {
			return err
		}
	}
	for !s.writeMu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer s.writeMu.Unlock()
	return s.spriteWriteTx(ctx, func(tx *sql.Tx) error {
		if casTxBeginHook != nil {
			casTxBeginHook()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// A no-op insert acquires the reserved writer lock even when the key exists.
		if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", spriteKey(expected.ID), "{}"); err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key=?", spriteKey(expected.ID)).Scan(&raw); err != nil {
			return err
		}
		var current SpriteState
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return err
		}
		if !reflect.DeepEqual(current, old) {
			return ErrSpriteChanged
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+movieCols+" FROM movies WHERE id=?", expected.ID)
		if err != nil {
			return err
		}
		if !rows.Next() {
			rows.Close()
			return ErrSpriteChanged
		}
		movie, err := movieScan(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(movie, expected) {
			return ErrSpriteChanged
		}
		var root string
		if err := tx.QueryRowContext(ctx, "SELECT path FROM libraries WHERE id=? AND enabled=1", expected.LibraryID).Scan(&root); err != nil {
			return err
		}
		if root != libraryRoot {
			return ErrSpriteChanged
		}
		if files.Commit != nil {
			// Constant-time publish only; a filesystem read here would pin
			// writeMu and the SQLite write lock for the whole read.
			if err := files.Commit(); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE kv SET value=? WHERE key=?", string(payload), spriteKey(expected.ID))
		return err
	})
}

// CompareAndSwapSprite is the single-phase form, kept for callers that need no
// Prepare phase. verify runs inside the write transaction, so it must stay
// constant-time; filesystem reads belong in CompareAndSwapSpriteFiles' Prepare
// phase.
func (s *Store) CompareAndSwapSprite(ctx context.Context, expected Movie, libraryRoot string, old, next SpriteState, verify func() error) error {
	return s.CompareAndSwapSpriteFiles(ctx, expected, libraryRoot, old, next, SpriteFiles{Commit: verify})
}
