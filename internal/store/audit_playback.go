package store

// PlaybackAuditSummary 是管理端防火墙页的播放记录合并行：同一用户对同一影片只留一行。
// PlayCount 是不同播放会话数（COUNT(DISTINCT session_key)），其余字段取该组合下 id 最大的事件。
type PlaybackAuditSummary struct {
	UserID        int64  `json:"user_id"`
	UserName      string `json:"user_name"`
	MovieID       int64  `json:"movie_id"`
	MovieTitle    string `json:"movie_title"`
	PlayCount     int    `json:"play_count"`
	LastAt        string `json:"last_at"`
	PositionTicks int64  `json:"position_ticks"`
	LastKind      string `json:"last_kind"`
}

// RecentPlaybackSummaries 按最近播放时间倒序返回合并后的播放记录，并返回合并行总数。
// userID=0 表示全部用户；用户被删除后行仍保留，UserName 为空由调用方显示“已删除用户”。
func (s *Store) RecentPlaybackSummaries(userID int64, start, limit int) ([]PlaybackAuditSummary, int, error) {
	const grouped = `FROM playback_events e
		LEFT JOIN playback_sessions s ON s.user_id=e.user_id AND s.session_key=e.session_key
		WHERE (?=0 OR e.user_id=?)
		GROUP BY e.user_id,COALESCE(s.movie_id,0)`
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 `+grouped+`)`, userID, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT g.user_id,COALESCE(u.name,''),g.movie_id,COALESCE(m.title,''),
		g.play_count,last.created_at,last.position_ticks,last.kind
		FROM (SELECT e.user_id AS user_id,COALESCE(s.movie_id,0) AS movie_id,
			COUNT(DISTINCT e.session_key) AS play_count,MAX(e.id) AS last_id
			`+grouped+`) g
		JOIN playback_events last ON last.id=g.last_id
		LEFT JOIN users u ON u.id=g.user_id
		LEFT JOIN movies m ON m.id=g.movie_id
		ORDER BY g.last_id DESC LIMIT ? OFFSET ?`, userID, userID, limit, start)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []PlaybackAuditSummary{}
	for rows.Next() {
		var e PlaybackAuditSummary
		if err := rows.Scan(&e.UserID, &e.UserName, &e.MovieID, &e.MovieTitle, &e.PlayCount, &e.LastAt, &e.PositionTicks, &e.LastKind); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
