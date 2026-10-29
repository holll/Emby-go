package store

import (
	"database/sql"
	"errors"
)

// SetUserLibraries 覆盖式设置某用户可见的媒体库。
func (s *Store) SetUserLibraries(userID int64, libraryIDs []int64) error {
	return s.writeTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM user_libraries WHERE user_id=?`, userID); err != nil {
			return err
		}
		for _, libraryID := range libraryIDs {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO user_libraries(user_id,library_id) VALUES(?,?)`, userID, libraryID); err != nil {
				return err
			}
		}
		return nil
	})
}

// UserLibraryIDs 返回某用户被授权的媒体库 id；管理员不在此表限制范围。
func (s *Store) UserLibraryIDs(userID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT library_id FROM user_libraries WHERE user_id=? ORDER BY library_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CanAccessLibrary 判断用户能否访问某个媒体库；管理员恒为 true。
func (s *Store) CanAccessLibrary(userID int64, admin bool, libraryID int64) (bool, error) {
	if admin {
		return true, nil
	}
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM user_libraries WHERE user_id=? AND library_id=?`, userID, libraryID).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
