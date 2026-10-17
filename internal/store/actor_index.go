package store

import (
	"database/sql"
	"strings"
	"time"
)

// SetIndexedActorAvatar preserves SetActorAvatar's empty-field/upsert semantics,
// but commits its global and every referencing library's version atomically.
// The actor can belong to multiple movies and libraries; library 0 is a real
// domain, never a sentinel. Legacy SetActorAvatar remains unversioned.
func (s *Store) SetIndexedActorAvatar(name, url, tag string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var global int64
	err := s.writeTx(func(tx *sql.Tx) error {
		var err error
		global, err = bumpGlobalVersionTx(tx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO actors(name,avatar_url,avatar_tag,updated_at) VALUES(?,?,?,?)
   ON CONFLICT(name) DO UPDATE SET
    avatar_url=CASE WHEN excluded.avatar_url<>'' THEN excluded.avatar_url ELSE actors.avatar_url END,
    avatar_tag=CASE WHEN excluded.avatar_tag<>'' THEN excluded.avatar_tag ELSE actors.avatar_tag END`, name, url, tag, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT DISTINCT m.library_id FROM movies m JOIN movie_actors ma ON ma.movie_id=m.id WHERE ma.actor_name=?`, name)
		if err != nil {
			return err
		}
		var libraries []int64
		for rows.Next() {
			var id sql.NullInt64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if id.Valid {
				libraries = append(libraries, id.Int64)
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, id := range libraries {
			if err = bumpLibraryVersionTx(tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.publishVersion(global)
	}
	return err
}
