package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"
)

// N-04：Webhook 投递历史。只存非秘密字段，用于事后核对哪次投递送达/失败；
// 不存地址、请求体、响应体或任何响应头，也不参与重发。
const webhookDeliveriesMigrationVersion = 20

const migration20Statement = `CREATE TABLE IF NOT EXISTS webhook_deliveries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event TEXT NOT NULL,
	item_id TEXT NOT NULL DEFAULT '',
	target_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	error_code TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
)`

// 留存上限：条数与时间任一满足即淘汰，避免高频播放事件无限增长。
const (
	WebhookDeliveryRetentionRows = 1000
	WebhookDeliveryRetentionDays = 7
)

func migration20Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(migration20Statement)))
}

func applyMigration20(tx *sql.Tx) error {
	if _, err := tx.Exec(migration20Statement); err != nil {
		return fmt.Errorf("schema migration %d: %w", webhookDeliveriesMigrationVersion, err)
	}
	return nil
}

// WebhookDelivery 是一行投递结果。字段白名单固定，不随事件负载扩展。
type WebhookDelivery struct {
	ID        int64  `json:"id"`
	Event     string `json:"event"`
	ItemID    string `json:"item_id"`
	TargetID  string `json:"target_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	CreatedAt string `json:"created_at"`
}

// RecordWebhookDeliveries 批量写入一次事件的全部目标结果，并按条数/时间淘汰。
// 写失败由调用方决定是否只记日志，不回头修改业务结果。
func (s *Store) RecordWebhookDeliveries(items []WebhookDelivery) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("record webhook deliveries: %w", err)
	}
	defer tx.Rollback()
	for _, item := range items {
		if _, err := tx.Exec(`INSERT INTO webhook_deliveries(event,item_id,target_id,status,error_code,created_at) VALUES(?,?,?,?,?,?)`,
			item.Event, item.ItemID, item.TargetID, item.Status, item.ErrorCode, item.CreatedAt); err != nil {
			return fmt.Errorf("record webhook delivery: %w", err)
		}
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -WebhookDeliveryRetentionDays).Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE created_at < ?`, cutoff); err != nil {
		return fmt.Errorf("prune webhook deliveries by age: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE id NOT IN (
		SELECT id FROM webhook_deliveries ORDER BY id DESC LIMIT ?)`, WebhookDeliveryRetentionRows); err != nil {
		return fmt.Errorf("prune webhook deliveries by count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record webhook deliveries: %w", err)
	}
	return nil
}

// ListWebhookDeliveries 按时间倒序返回最近 limit 条。
func (s *Store) ListWebhookDeliveries(limit int) ([]WebhookDelivery, error) {
	rows, err := s.db.Query(`SELECT id,event,item_id,target_id,status,error_code,created_at FROM webhook_deliveries ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	defer rows.Close()
	items := []WebhookDelivery{}
	for rows.Next() {
		var item WebhookDelivery
		if err := rows.Scan(&item.ID, &item.Event, &item.ItemID, &item.TargetID, &item.Status, &item.ErrorCode, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("list webhook deliveries: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	return items, nil
}

// ClearWebhookDeliveries 删除全部投递历史，返回删除行数。
func (s *Store) ClearWebhookDeliveries() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM webhook_deliveries`)
	if err != nil {
		return 0, fmt.Errorf("clear webhook deliveries: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("clear webhook deliveries: %w", err)
	}
	return removed, nil
}
