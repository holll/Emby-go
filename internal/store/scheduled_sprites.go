package store

import (
	"context"
	"database/sql"
	"errors"
)

// ScheduledSpriteTarget is a bounded-page projection; movie metadata is never
// rewritten by target selection. Only enabled libraries and STRM sources qualify.
type ScheduledSpriteTarget struct{ ID, LibraryID int64 }

func (s *Store) ScheduledSpriteScope(ctx context.Context, libraryID int64) (total int, maxID int64, err error) {
	if libraryID < 0 {
		return 0, 0, errors.New("invalid sprite library scope")
	}
	if libraryID > 0 {
		var enabled int
		if err = s.db.QueryRowContext(ctx, "SELECT enabled FROM libraries WHERE id=?", libraryID).Scan(&enabled); err != nil {
			return
		}
		if enabled != 1 {
			return 0, 0, sql.ErrNoRows
		}
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(m.id),0) FROM movies m JOIN libraries l ON l.id=m.library_id WHERE l.enabled=1 AND lower(substr(m.source_path,-5))='.strm' AND (?=0 OR m.library_id=?)`, libraryID, libraryID).Scan(&total, &maxID)
	return
}

func (s *Store) ScheduledSpriteTargets(ctx context.Context, libraryID, after, maxID int64, limit int) ([]ScheduledSpriteTarget, error) {
	if libraryID < 0 || after < 0 || maxID < 0 || limit < 1 || limit > 200 {
		return nil, errors.New("invalid sprite target page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.library_id FROM movies m JOIN libraries l ON l.id=m.library_id WHERE l.enabled=1 AND lower(substr(m.source_path,-5))='.strm' AND (?=0 OR m.library_id=?) AND m.id>? AND m.id<=? ORDER BY m.id LIMIT ?`, libraryID, libraryID, after, maxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]ScheduledSpriteTarget, 0, limit)
	for rows.Next() {
		var target ScheduledSpriteTarget
		if err = rows.Scan(&target.ID, &target.LibraryID); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
