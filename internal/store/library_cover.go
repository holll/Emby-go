package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// LibraryCover 是一条"已应用"的媒体库封面记录。
type LibraryCover struct {
	LibraryID  int64
	Path       string
	SourceHash string
	Algorithm  string
	Width      int
	Height     int
	AppliedAt  string
}

// AppliedLibraryCover 取单个库已应用的封面；不存在返回 (零值, false, nil)。
func (s *Store) AppliedLibraryCover(libraryID int64) (LibraryCover, bool, error) {
	var cover LibraryCover
	err := s.db.QueryRow(`SELECT library_id,path,source_hash,algorithm,width,height,applied_at
		FROM library_covers WHERE library_id=?`, libraryID).
		Scan(&cover.LibraryID, &cover.Path, &cover.SourceHash, &cover.Algorithm, &cover.Width, &cover.Height, &cover.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryCover{}, false, nil
	}
	if err != nil {
		return LibraryCover{}, false, fmt.Errorf("applied library cover %d: %w", libraryID, err)
	}
	return cover, true, nil
}

// AppliedLibraryCovers 取全部已应用封面，按库 id 建索引。
func (s *Store) AppliedLibraryCovers() (map[int64]LibraryCover, error) {
	rows, err := s.db.Query(`SELECT library_id,path,source_hash,algorithm,width,height,applied_at
		FROM library_covers`)
	if err != nil {
		return nil, fmt.Errorf("applied library covers: %w", err)
	}
	defer rows.Close()
	covers := make(map[int64]LibraryCover)
	for rows.Next() {
		var cover LibraryCover
		if err := rows.Scan(&cover.LibraryID, &cover.Path, &cover.SourceHash, &cover.Algorithm,
			&cover.Width, &cover.Height, &cover.AppliedAt); err != nil {
			return nil, err
		}
		covers[cover.LibraryID] = cover
	}
	return covers, rows.Err()
}

// SetAppliedLibraryCover 写入或覆盖一条记录（library_id 为主键）。
func (s *Store) SetAppliedLibraryCover(cover LibraryCover) error {
	return s.writeTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO library_covers(library_id,path,source_hash,algorithm,width,height,applied_at)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(library_id) DO UPDATE SET path=excluded.path,source_hash=excluded.source_hash,
			algorithm=excluded.algorithm,width=excluded.width,height=excluded.height,applied_at=excluded.applied_at`,
			cover.LibraryID, cover.Path, cover.SourceHash, cover.Algorithm, cover.Width, cover.Height, cover.AppliedAt)
		if err != nil {
			return fmt.Errorf("set applied library cover %d: %w", cover.LibraryID, err)
		}
		return nil
	})
}

// ClearAppliedLibraryCover 删除一条记录；不存在时返回 nil。
func (s *Store) ClearAppliedLibraryCover(libraryID int64) error {
	return s.writeTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM library_covers WHERE library_id=?", libraryID); err != nil {
			return fmt.Errorf("clear applied library cover %d: %w", libraryID, err)
		}
		return nil
	})
}
