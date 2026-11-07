package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
	"emby-go/internal/webhook"
)

// N-04：按目标记录一次业务事件的投递结果。字段白名单固定，best-effort——
// 历史写失败只记日志，不回滚业务结果，也不触发重发。
func (a *App) recordWebhookDeliveries(event webhook.Event, result webhook.Result) {
	if len(result.Targets) == 0 {
		return
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	rows := make([]store.WebhookDelivery, 0, len(result.Targets))
	for _, outcome := range result.Targets {
		rows = append(rows, store.WebhookDelivery{
			Event: event.Event, ItemID: event.Item.Id, TargetID: outcome.ID,
			Status: outcome.Status, ErrorCode: outcome.ErrorCode, CreatedAt: createdAt,
		})
	}
	if err := a.db.RecordWebhookDeliveries(rows); err != nil {
		slog.Warn("Webhook 投递历史写入失败；结果仍以日志为准", "event", event.Event, "item_id", event.Item.Id, "error", err)
	}
}

// adminWebhookDeliveries 返回最近投递历史。只读，不探测接收端。
func (a *App) adminWebhookDeliveries(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit 必须是 1-500 的整数"})
			return
		}
		limit = n
	}
	items, err := a.db.ListWebhookDeliveries(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取 Webhook 投递历史失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"retention": gin.H{
			"max_rows": store.WebhookDeliveryRetentionRows,
			"days":     store.WebhookDeliveryRetentionDays,
		},
	})
}

// adminClearWebhookDeliveries 清空历史；历史不是投递队列，清除不影响重试或业务。
func (a *App) adminClearWebhookDeliveries(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !a.admitAdminOperation(c) {
		return
	}
	removed, err := a.db.ClearWebhookDeliveries()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清空 Webhook 投递历史失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": removed})
}
