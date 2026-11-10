package store

import "database/sql"

// SetIndexedFavoriteDataForUser 按账户写收藏，并返回同事务内读到的该用户快照。
func (s *Store) SetIndexedFavoriteDataForUser(userID, id int64, favorite bool) (UserData, error) {
	return s.writeIndexedUserData(userID, id, `INSERT INTO userdata(user_id,movie_id,is_favorite) VALUES(?,?,?)
		ON CONFLICT(user_id,movie_id) DO UPDATE SET is_favorite=excluded.is_favorite`, b2i(favorite))
}

func (s *Store) SetIndexedLikesDataForUser(userID, id int64, likes int) (UserData, error) {
	return s.writeIndexedUserData(userID, id, `INSERT INTO userdata(user_id,movie_id,likes) VALUES(?,?,?)
		ON CONFLICT(user_id,movie_id) DO UPDATE SET likes=excluded.likes`, likes)
}

func (s *Store) SetIndexedHideFromResumeDataForUser(userID, id int64, hidden bool) (UserData, error) {
	return s.writeIndexedUserData(userID, id, `INSERT INTO userdata(user_id,movie_id,hide_from_resume) VALUES(?,?,?)
		ON CONFLICT(user_id,movie_id) DO UPDATE SET hide_from_resume=excluded.hide_from_resume`, b2i(hidden))
}

func (s *Store) writeIndexedUserData(userID, id int64, statement string, value int) (UserData, error) {
	if userID <= 0 {
		userID = 1
	}
	var version int64
	var data UserData
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		// Obtain the writer before resolving the movie: no read-to-write snapshot
		// upgrade and no stale library/runtime after a concurrent movie change.
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		var libraryID sql.NullInt64
		var runtimeSeconds int64
		if err := tx.QueryRow("SELECT library_id,COALESCE(runtime_seconds,0) FROM movies WHERE id=? AND status IN ('success','manual')", id).Scan(&libraryID, &runtimeSeconds); err != nil {
			return err
		}
		if _, err := tx.Exec(statement, userID, id, value); err != nil {
			return err
		}
		data, err = readUserData(tx, userID, id)
		if err != nil {
			return err
		}
		if runtimeSeconds > 0 && data.PositionTicks > 0 {
			data.PlayedPercentage = float64(data.PositionTicks) / (float64(runtimeSeconds) * 10000000) * 100
		}
		if libraryID.Valid {
			return bumpLibraryVersionTx(tx, libraryID.Int64)
		}
		return nil
	})
	if err != nil {
		return UserData{}, err
	}
	s.publishVersion(version)
	return data, nil
}
