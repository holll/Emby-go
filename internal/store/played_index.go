package store

// SetIndexedPlayed changes only the explicit played flag, never playback counters
// or position. The actual visible movie's library and global version commit with it.
func (s *Store) SetIndexedPlayed(id int64, played bool) error {
	return s.SetIndexedPlayedForUser(1, id, played)
}

// SetIndexedPlayedForUser 按账户写播放标记，并返回同事务内读到的该用户快照。
func (s *Store) SetIndexedPlayedForUser(userID, id int64, played bool) error {
	_, err := s.SetIndexedPlayedDataForUser(userID, id, played)
	return err
}

// SetIndexedPlayedDataForUser 返回同事务内读取的 UserData 快照。
func (s *Store) SetIndexedPlayedDataForUser(userID, id int64, played bool) (UserData, error) {
	return s.writeIndexedUserData(userID, id, `INSERT INTO userdata(user_id,movie_id,played) VALUES(?,?,?)
		ON CONFLICT(user_id,movie_id) DO UPDATE SET played=excluded.played`, b2i(played))
}
