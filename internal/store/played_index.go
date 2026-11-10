package store

// SetIndexedPlayedDataForUser 返回同事务内读取的 UserData 快照。
func (s *Store) SetIndexedPlayedDataForUser(userID, id int64, played bool) (UserData, error) {
	return s.writeIndexedUserData(userID, id, `INSERT INTO userdata(user_id,movie_id,played) VALUES(?,?,?)
		ON CONFLICT(user_id,movie_id) DO UPDATE SET played=excluded.played`, b2i(played))
}
