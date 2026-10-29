package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const DefaultSpriteIntervalSeconds = 10
const MinSpriteIntervalSeconds = 10
const MaxSpriteIntervalSeconds = 86400

func librarySpriteKey(id int64) string { return fmt.Sprintf("library_sprite_interval:%d", id) }

func spriteIntervalValue(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < MinSpriteIntervalSeconds || n > MaxSpriteIntervalSeconds {
		return 0, errors.New("invalid library sprite interval")
	}
	return n, nil
}

// LibrarySpriteInterval reads only enabled libraries; an unset preference preserves
// the previous ten-second default. It never touches movie or preview state.
func (s *Store) LibrarySpriteInterval(ctx context.Context, id int64) (int, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(k.value,'10') FROM libraries l LEFT JOIN kv k ON k.key=? WHERE l.id=? AND l.enabled=1`, librarySpriteKey(id), id).Scan(&raw)
	if err != nil {
		return 0, err
	}
	return spriteIntervalValue(raw)
}

func (s *Store) LibrarySpriteIntervals(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,COALESCE(k.value,'10') FROM libraries l LEFT JOIN kv k ON k.key='library_sprite_interval:' || l.id WHERE l.enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]int)
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		n, err := spriteIntervalValue(raw)
		if err != nil {
			return nil, err
		}
		result[id] = n
	}
	return result, rows.Err()
}

// SetLibrarySpriteInterval is one atomic statement, including the live-library
// check. Settings affect future admitted tasks, not existing generations.
func (s *Store) SetLibrarySpriteInterval(ctx context.Context, id int64, seconds int) error {
	if id <= 0 || seconds < MinSpriteIntervalSeconds || seconds > MaxSpriteIntervalSeconds {
		return errors.New("interval_seconds must be 10 to 86400")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO kv(key,value) SELECT ?,? WHERE EXISTS(SELECT 1 FROM libraries WHERE id=? AND enabled=1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, librarySpriteKey(id), strconv.Itoa(seconds), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
