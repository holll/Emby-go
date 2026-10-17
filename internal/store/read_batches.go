package store

import (
	"database/sql"
	"strings"
)

const readIDBatchSize = 500

type batchQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

// Each ID is read once: repeated IDs must not duplicate actor slices. Small
// requests keep one statement; larger requests share a read snapshot across chunks.
// The callback must close its rows before returning so a single connection works.
func (s *Store) readIDBatches(ids []int64, read func(batchQueryer, string, []any) error) error {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	var q batchQueryer = s.db
	var tx *sql.Tx
	if len(unique) > readIDBatchSize {
		var err error
		tx, err = s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		q = tx
	}
	for start := 0; start < len(unique); start += readIDBatchSize {
		end := min(start+readIDBatchSize, len(unique))
		args := make([]any, end-start)
		for i, id := range unique[start:end] {
			args[i] = id
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
		if err := read(q, marks, args); err != nil {
			return err
		}
	}
	if tx != nil {
		return tx.Rollback()
	}
	return nil
}
