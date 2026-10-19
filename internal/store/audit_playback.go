package store

// PlaybackAuditEvent 是管理端活动记录里的一条播放事件（含用户与影片快照）。
type PlaybackAuditEvent struct {
	ID            int64  `json:"id"`
	SessionKey    string `json:"session_key"`
	UserID        int64  `json:"user_id"`
	UserName      string `json:"user_name"`
	MovieID       int64  `json:"movie_id"`
	MovieTitle    string `json:"movie_title"`
	Kind          string `json:"kind"`
	PositionTicks int64  `json:"position_ticks"`
	CreatedAt     string `json:"created_at"`
}

// RecentPlaybackEvents 按时间倒序返回播放事件；userID=0 表示全部用户。
// 用户被删除后事件仍保留，UserName 为空由调用方显示“已删除用户”。
func (s *Store) RecentPlaybackEvents(userID int64, limit int) ([]PlaybackAuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT e.id,COALESCE(e.session_key,''),e.user_id,COALESCE(u.name,''),
		COALESCE(s.movie_id,0),COALESCE(m.title,''),e.kind,e.position_ticks,e.created_at
		FROM playback_events e
		LEFT JOIN playback_sessions s ON s.user_id=e.user_id AND s.session_key=e.session_key
		LEFT JOIN users u ON u.id=e.user_id
		LEFT JOIN movies m ON m.id=s.movie_id`
	args := []any{}
	if userID > 0 {
		query += ` WHERE e.user_id=?`
		args = append(args, userID)
	}
	query += ` ORDER BY e.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlaybackAuditEvent{}
	for rows.Next() {
		var e PlaybackAuditEvent
		if err := rows.Scan(&e.ID, &e.SessionKey, &e.UserID, &e.UserName, &e.MovieID, &e.MovieTitle, &e.Kind, &e.PositionTicks, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
