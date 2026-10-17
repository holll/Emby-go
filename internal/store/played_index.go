package store

import "database/sql"

// SetIndexedPlayed changes only the explicit played flag, never playback counters
// or position. The actual visible movie's library and global version commit with it.
func (s *Store) SetIndexedPlayed(id int64, played bool) error {
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		version, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		var libraryID sql.NullInt64
		if err := tx.QueryRow("SELECT library_id FROM movies WHERE id=? AND status IN ('success','manual')", id).Scan(&libraryID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO userdata(movie_id,played) VALUES(?,?)
			ON CONFLICT(movie_id) DO UPDATE SET played=excluded.played`, id, b2i(played)); err != nil {
			return err
		}
		if libraryID.Valid {
			return bumpLibraryVersionTx(tx, libraryID.Int64)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publishVersion(version)
	return nil
}
