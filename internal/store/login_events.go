package store

import (
	"time"
)

// LoginEvent 是一次登录尝试的审计记录（成功或失败）。
type LoginEvent struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
	CreatedAt string `json:"created_at"`
}

// RecordLoginEvent 记录一次登录尝试。name 保存尝试的用户名（失败时账户可能不存在）。
// 审计失败不应阻止登录，调用方只记录日志。
func (s *Store) RecordLoginEvent(userID int64, name, ip string, success bool, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.db.Exec(`INSERT INTO login_events(user_id,name,ip,success,created_at) VALUES(?,?,?,?,?)`,
		uid, name, ip, b2i(success), now.UTC().Format(time.RFC3339))
	return err
}

// LoginEvents 按时间倒序返回最近 limit 条登录记录；userID=0 表示全部账户。
func (s *Store) LoginEvents(userID int64, limit int) ([]LoginEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id,COALESCE(user_id,0),name,COALESCE(ip,''),success,created_at FROM login_events`
	args := []any{}
	if userID > 0 {
		query += ` WHERE user_id=?`
		args = append(args, userID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoginEvent
	for rows.Next() {
		var e LoginEvent
		var success int
		if err := rows.Scan(&e.ID, &e.UserID, &e.Name, &e.IP, &success, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Success = success != 0
		out = append(out, e)
	}
	return out, rows.Err()
}
