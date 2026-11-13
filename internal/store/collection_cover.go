package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// CollectionCover 是一条"已应用"的合集封面记录。
type CollectionCover struct {
	Collection string
	Path       string
	SourceHash string
	Algorithm  string
	Width      int
	Height     int
	AppliedAt  string
}

// AppliedCover 取单个合集已应用的封面；不存在返回 (零值, false, nil)。
func (s *Store) AppliedCover(collection string) (CollectionCover, bool, error) {
	var cover CollectionCover
	err := s.db.QueryRow(`SELECT collection,path,source_hash,algorithm,width,height,applied_at
		FROM collection_covers WHERE collection=?`, collection).
		Scan(&cover.Collection, &cover.Path, &cover.SourceHash, &cover.Algorithm, &cover.Width, &cover.Height, &cover.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CollectionCover{}, false, nil
	}
	if err != nil {
		return CollectionCover{}, false, fmt.Errorf("applied cover %q: %w", collection, err)
	}
	return cover, true, nil
}

// AppliedCovers 取全部已应用封面，按 collection 建索引。
func (s *Store) AppliedCovers() (map[string]CollectionCover, error) {
	rows, err := s.db.Query(`SELECT collection,path,source_hash,algorithm,width,height,applied_at
		FROM collection_covers`)
	if err != nil {
		return nil, fmt.Errorf("applied covers: %w", err)
	}
	defer rows.Close()
	covers := make(map[string]CollectionCover)
	for rows.Next() {
		var cover CollectionCover
		if err := rows.Scan(&cover.Collection, &cover.Path, &cover.SourceHash, &cover.Algorithm,
			&cover.Width, &cover.Height, &cover.AppliedAt); err != nil {
			return nil, err
		}
		covers[cover.Collection] = cover
	}
	return covers, rows.Err()
}

// SetAppliedCover 写入或覆盖一条记录（collection 为主键）。
func (s *Store) SetAppliedCover(cover CollectionCover) error {
	return s.writeTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO collection_covers(collection,path,source_hash,algorithm,width,height,applied_at)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(collection) DO UPDATE SET path=excluded.path,source_hash=excluded.source_hash,
			algorithm=excluded.algorithm,width=excluded.width,height=excluded.height,applied_at=excluded.applied_at`,
			cover.Collection, cover.Path, cover.SourceHash, cover.Algorithm, cover.Width, cover.Height, cover.AppliedAt)
		if err != nil {
			return fmt.Errorf("set applied cover %q: %w", cover.Collection, err)
		}
		return nil
	})
}

// ClearAppliedCover 删除一条记录；不存在时返回 nil。
func (s *Store) ClearAppliedCover(collection string) error {
	return s.writeTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM collection_covers WHERE collection=?", collection); err != nil {
			return fmt.Errorf("clear applied cover %q: %w", collection, err)
		}
		return nil
	})
}
