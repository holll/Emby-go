package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"emby-go/internal/config"
	"emby-go/internal/store"
	"emby-go/internal/webhook"
	"github.com/gin-gonic/gin"
)

const settingWebhook = "webhook.config.v1"

type storedWebhookConfig struct {
	Version int                  `json:"version"`
	Config  config.WebhookConfig `json:"config"`
}

func normalizeWebhookConfig(cfg config.WebhookConfig) config.WebhookConfig {
	cfg.Events = append([]string(nil), cfg.Events...)
	if len(cfg.Events) == 0 {
		cfg.Events = []string{webhook.ItemDeleted}
	}
	if cfg.Format == "" {
		cfg.Format = "json"
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 5
	}
	return cfg
}

// Database overrides are authoritative, including explicit disable/URL clear.
// Corrupt or unreadable stored configuration must not reactivate YAML settings.
func loadWebhookConfig(db *store.Store, fallback config.WebhookConfig) (config.WebhookConfig, error) {
	values, err := db.Settings()
	if err != nil {
		return config.WebhookConfig{}, errors.New("webhook settings unavailable")
	}
	raw, exists := values[settingWebhook]
	if !exists {
		return normalizeWebhookConfig(fallback), nil
	}
	var stored storedWebhookConfig
	if json.Unmarshal([]byte(raw), &stored) != nil || stored.Version != 1 || stored.Config.Validate() != nil {
		return config.WebhookConfig{}, errors.New("webhook settings invalid")
	}
	return normalizeWebhookConfig(stored.Config), nil
}

func (a *App) webhookSender() *webhook.Sender {
	a.webhookMu.RLock()
	defer a.webhookMu.RUnlock()
	return a.webhook
}

func (a *App) closeWebhook() {
	a.webhookUpdateMu.Lock()
	defer a.webhookUpdateMu.Unlock()
	a.webhookClosed = true
	a.webhookMu.Lock()
	old := a.webhook
	a.webhook = nil
	a.webhookMu.Unlock()
	old.Close()
}

func webhookConfigETag(cfg config.WebhookConfig) string {
	raw, _ := json.Marshal(cfg)
	return fmt.Sprintf("\"wh1-%x\"", sha256.Sum256(raw))
}

func webhookSettingsView(cfg config.WebhookConfig) gin.H {
	return gin.H{"enabled": cfg.Enabled, "url_configured": cfg.URL != "", "events": cfg.Events,
		"format": cfg.Format, "timeout_seconds": cfg.TimeoutSeconds, "allow_private": cfg.AllowPrivate,
		"config_etag": webhookConfigETag(cfg)}
}

func (a *App) adminWebhookSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	a.webhookMu.RLock()
	cfg := a.webhookConfig
	a.webhookMu.RUnlock()
	c.Header("ETag", webhookConfigETag(cfg))
	c.JSON(http.StatusOK, webhookSettingsView(cfg))
}

type webhookSettingsRequest struct {
	Enabled        *bool     `json:"enabled"`
	URL            *string   `json:"url"`
	ClearURL       bool      `json:"clear_url"`
	Events         *[]string `json:"events"`
	Format         *string   `json:"format"`
	TimeoutSeconds *int      `json:"timeout_seconds"`
	AllowPrivate   *bool     `json:"allow_private"`
	ConfirmDelete  bool      `json:"confirm_delete"`
}

func webhookSettingsError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"code": code, "error": message})
}

func (a *App) adminSaveWebhookSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetHeader("If-Match") == "" {
		webhookSettingsError(c, 428, "WEBHOOK_PRECONDITION_REQUIRED", "请重新加载 Webhook 配置后保存")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 16385))
	var req webhookSettingsRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err != nil || len(raw) > 16384 || dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF ||
		req.Enabled == nil || req.Events == nil || req.Format == nil || req.TimeoutSeconds == nil || req.AllowPrivate == nil {
		webhookSettingsError(c, 400, "WEBHOOK_INVALID_CONFIG", "Webhook 配置格式无效")
		return
	}
	a.webhookUpdateMu.Lock()
	defer a.webhookUpdateMu.Unlock()
	if a.webhookClosed || a.rootCtx.Err() != nil {
		webhookSettingsError(c, 503, "WEBHOOK_SETTINGS_UNAVAILABLE", "服务正在关闭，未保存配置")
		return
	}
	a.webhookMu.RLock()
	oldCfg := a.webhookConfig
	a.webhookMu.RUnlock()
	if c.GetHeader("If-Match") != webhookConfigETag(oldCfg) {
		webhookSettingsError(c, 412, "WEBHOOK_CONFIG_CHANGED", "Webhook 配置已被修改；请重新加载后核对，不要覆盖旧配置")
		return
	}
	cfg := config.WebhookConfig{Enabled: *req.Enabled, URL: oldCfg.URL, Events: append([]string(nil), (*req.Events)...),
		Format: *req.Format, TimeoutSeconds: *req.TimeoutSeconds, AllowPrivate: *req.AllowPrivate}
	if req.ClearURL {
		cfg.URL = ""
	}
	if req.URL != nil && strings.TrimSpace(*req.URL) != "" {
		if req.ClearURL {
			webhookSettingsError(c, 400, "WEBHOOK_INVALID_CONFIG", "清除与替换接收地址不能同时选择")
			return
		}
		cfg.URL = strings.TrimSpace(*req.URL)
	}
	if len(cfg.Events) == 0 || len(cfg.Events) > 13 || cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 30 ||
		(cfg.Format != "json" && cfg.Format != "form") || len(cfg.URL) > 4096 || cfg.Validate() != nil {
		webhookSettingsError(c, 400, "WEBHOOK_INVALID_CONFIG", "配置无效：检查事件、HTTP(S) 地址、内网许可、编码格式及 1–30 秒超时")
		return
	}
	// Validate stored draft URLs too. Validation is offline and never sends a request.
	if cfg.URL != "" {
		probe := cfg
		probe.Enabled = true
		if probe.Validate() != nil {
			webhookSettingsError(c, 400, "WEBHOOK_INVALID_CONFIG", "接收地址无效或需要显式允许内网地址")
			return
		}
	}
	for _, event := range cfg.Events {
		if cfg.Enabled && event == webhook.ItemDeleted && !req.ConfirmDelete {
			webhookSettingsError(c, 400, "WEBHOOK_DELETE_CONFIRMATION_REQUIRED", "启用删除通知需明确确认：SmartStrm 可能删除远端整个目录")
			return
		}
	}
	next, err := webhook.New(cfg)
	if err != nil {
		webhookSettingsError(c, 400, "WEBHOOK_INVALID_CONFIG", "Webhook 配置无效")
		return
	}
	data, _ := json.Marshal(storedWebhookConfig{Version: 1, Config: cfg})
	if err = a.db.SetSetting(settingWebhook, string(data)); err != nil {
		next.Close()
		webhookSettingsError(c, 500, "WEBHOOK_SETTINGS_UNAVAILABLE", "Webhook 配置保存失败，当前配置未变")
		return
	}
	// Persist first, then atomically publish an immutable sender/config pair.
	// Already-started deliveries may finish with their original configuration.
	a.webhookMu.Lock()
	old := a.webhook
	a.webhook = next
	a.webhookConfig = cfg
	a.webhookMu.Unlock()
	old.Close()
	c.Header("ETag", webhookConfigETag(cfg))
	c.JSON(http.StatusOK, webhookSettingsView(cfg))
}
