package store

import "time"

// AdminAuditEvent 是一次管理端写操作（POST/PUT/PATCH/DELETE）的只读审计记录。
// 与登录审计一样按 90 天/5 万条裁剪，避免长期运行无限增长。
type AdminAuditEvent struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	CreatedAt string `json:"created_at"`
}

const (
	adminAuditRetention = 90 * 24 * time.Hour
	adminAuditMaxRows   = 50000
)

// RecordAdminAudit 写入一条管理操作审计；失败由调用方记录日志，不影响业务响应。
func (s *Store) RecordAdminAudit(userID int64, name, method, path string, status int, now time.Time) error {
	createdAt := now.UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(`INSERT INTO admin_audit_events(user_id,name,method,path,status,created_at) VALUES(?,?,?,?,?,?)`,
		userID, name, method, path, status, createdAt); err != nil {
		return err
	}
	cutoff := now.Add(-adminAuditRetention).UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(`DELETE FROM admin_audit_events WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM admin_audit_events WHERE id NOT IN (SELECT id FROM admin_audit_events ORDER BY id DESC LIMIT ?)`, adminAuditMaxRows)
	return err
}

// AdminAuditEvents 返回最近的管理操作（新的在前）；limit<=0 或 >500 时取 100。
func (s *Store) AdminAuditEvents(limit int) ([]AdminAuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,user_id,COALESCE(name,''),method,path,status,created_at FROM admin_audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AdminAuditEvent, 0)
	for rows.Next() {
		var e AdminAuditEvent
		if err := rows.Scan(&e.ID, &e.UserID, &e.Name, &e.Method, &e.Path, &e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
