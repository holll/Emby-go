package store

import "database/sql"

type userDataQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// readUserData does not swallow a missing row. Store.Data alone preserves the
// legacy missing-row default; a post-mutation response must read a real row.
func readUserData(q userDataQuerier, id int64) (UserData, error) {
	var d UserData
	var played, favorite, likes, hidden int
	err := q.QueryRow(`SELECT position_ticks,play_count,played,COALESCE(last_played_at,''),COALESCE(last_stopped_ticks,-1),
		COALESCE(is_favorite,0),COALESCE(likes,0),COALESCE(hide_from_resume,0) FROM userdata WHERE movie_id=?`, id).Scan(&d.PositionTicks, &d.PlayCount, &played, &d.LastPlayedAt, &d.StoppedTicks, &favorite, &likes, &hidden)
	d.Played = played != 0
	d.IsFavorite = favorite != 0
	d.Likes = likes
	d.HideFromResume = hidden != 0
	return d, err
}
