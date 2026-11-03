package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/webhook"
	"github.com/gin-gonic/gin"
)

// Process-wide, nonblocking cap: test traffic cannot create an unbounded queue
// or borrow capacity from ordinary business-event delivery.
var webhookTestSlots = make(chan struct{}, 2)

type webhookTestResponse struct {
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	ErrorCode  string `json:"error_code"`
	Message    string `json:"message"`
}

func (a *App) adminWebhookTest(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	invalid := func() {
		c.JSON(http.StatusBadRequest, webhookTestResponse{Status: "failed", ErrorCode: "invalid_draft", Message: "Webhook 测试参数无效，请检查地址、格式及超时时间"})
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, (8<<10)+1))
	if err != nil || len(raw) > 8<<10 {
		invalid()
		return
	}
	var draft struct {
		URL            string `json:"url"`
		Format         string `json:"format"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		AllowPrivate   bool   `json:"allow_private"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&draft) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) {
		invalid()
		return
	}
	cfg := config.WebhookConfig{Enabled: true, URL: strings.TrimSpace(draft.URL), Format: draft.Format, TimeoutSeconds: draft.TimeoutSeconds, AllowPrivate: draft.AllowPrivate}
	if cfg.URL == "" || len(cfg.URL) > 4096 || cfg.Validate() != nil {
		invalid()
		return
	}
	select {
	case webhookTestSlots <- struct{}{}:
		defer func() { <-webhookTestSlots }()
	default:
		c.JSON(http.StatusOK, webhookTestResponse{Status: "failed", ErrorCode: "busy", Message: "Webhook 测试繁忙，请稍后重试"})
		return
	}
	sender, err := webhook.New(cfg)
	if err != nil {
		invalid()
		return
	}
	defer sender.Close()
	ctx, cancel := context.WithTimeout(c.Request.Context(), cfg.Timeout())
	defer cancel()
	started := time.Now()
	err = sender.SendTest(ctx, webhook.Server{Id: a.serverID, Name: a.serverName})
	result := webhookTestResponse{Status: "sent", DurationMS: time.Since(started).Milliseconds(), Message: "接收端已确认测试消息；仅表示接收确认，不代表远端业务执行完成"}
	if err != nil {
		result.Status = "failed"
		result.ErrorCode = webhook.DeliveryErrorCode(err)
		result.Message = webhookFailureReason(result.ErrorCode)
	}
	c.JSON(http.StatusOK, result)
}
