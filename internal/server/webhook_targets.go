package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"emby-go/internal/config"
	"emby-go/internal/store"
	"emby-go/internal/webhook"
	"github.com/gin-gonic/gin"
)

// R3：多目标 Webhook 的 v2 配置。settings 表仍是唯一持久层；v2 一旦存在就
// 完全权威（包括 targets:[]），损坏时关闭投递而不是回落旧地址。
const (
	settingWebhookV2        = "webhook.config.v2"
	webhookTargetsBodyLimit = 64 << 10
	legacyWebhookTargetID   = "wh_legacy"
)

type webhookTargetsConfig struct {
	Version int              `json:"version"`
	Targets []webhook.Target `json:"targets"`
}

func legacyWebhookTargetSpec(cfg config.WebhookConfig) webhook.Target {
	return webhook.Target{
		ID:      legacyWebhookTargetID,
		Name:    "默认目标",
		Enabled: cfg.Enabled,
		URL:     cfg.URL,
		Events:  append([]string(nil), cfg.Events...),
		Format:  cfg.Format,
		// 旧空事件仍只代表删除事件，不能迁移成全事件。
		TimeoutSeconds: cfg.TimeoutSeconds,
		AllowPrivate:   cfg.AllowPrivate,
	}
}

// loadWebhookTargets 读取运行配置：v2 权威；否则把 v1/YAML 封装为唯一
// legacy 目标（仅内存适配，首次成功保存才写 v2）。
func loadWebhookTargets(db *store.Store, fallback config.WebhookConfig) ([]webhook.Target, []webhook.Target, config.WebhookConfig, bool, error) {
	values, err := db.Settings()
	if err != nil {
		return nil, nil, config.WebhookConfig{}, false, errors.New("webhook settings unavailable")
	}
	if raw, ok := values[settingWebhookV2]; ok {
		var stored webhookTargetsConfig
		dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
		dec.DisallowUnknownFields()
		if dec.Decode(&stored) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) || stored.Version != 2 ||
			len(stored.Targets) > webhook.MaxTargets {
			return nil, nil, config.WebhookConfig{}, true, errors.New("webhook multi-target settings invalid")
		}
		seen := make(map[string]bool, len(stored.Targets))
		for i := range stored.Targets {
			spec, err := webhook.NormalizeTarget(stored.Targets[i])
			if err != nil || seen[spec.ID] {
				return nil, nil, config.WebhookConfig{}, true, errors.New("webhook multi-target settings invalid")
			}
			seen[spec.ID] = true
			stored.Targets[i] = spec
		}
		// v2 存在时旧单目标接口只保留升级提示；运行态不再读 v1。
		return stored.Targets, stored.Targets, config.WebhookConfig{}, true, nil
	}
	legacyCfg, err := loadWebhookConfig(db, fallback)
	if err != nil {
		return nil, nil, config.WebhookConfig{}, false, err
	}
	spec, err := webhook.NormalizeTarget(legacyWebhookTargetSpec(legacyCfg))
	if err != nil {
		return nil, nil, config.WebhookConfig{}, false, errors.New("webhook settings invalid")
	}
	return []webhook.Target{spec}, []webhook.Target{spec}, legacyCfg, false, nil
}

func webhookTargetsETag(specs []webhook.Target) string {
	raw, _ := json.Marshal(webhookTargetsConfig{Version: 2, Targets: specs})
	return fmt.Sprintf("\"wh2-%x\"", sha256.Sum256(raw))
}

func webhookTargetsView(specs []webhook.Target, multi bool) gin.H {
	out := make([]gin.H, 0, len(specs))
	for _, spec := range specs {
		out = append(out, gin.H{
			"id": spec.ID, "name": spec.Name, "enabled": spec.Enabled,
			"url": spec.URL, "url_configured": spec.URL != "", "events": spec.Events, "format": spec.Format,
			"timeout_seconds": spec.TimeoutSeconds, "allow_private": spec.AllowPrivate,
		})
	}
	return gin.H{"version": 2, "targets": out, "config_etag": webhookTargetsETag(specs), "multi_target": multi}
}

// publishWebhook 原子替换运行快照；旧快照的已开始投递自行结束。
func (a *App) publishWebhook(d *webhook.Dispatcher, specs []webhook.Target, legacy config.WebhookConfig, multi bool) {
	a.webhookMu.Lock()
	old := a.webhook
	a.webhook = d
	a.webhookTargets = append([]webhook.Target(nil), specs...)
	a.webhookConfig = legacy
	a.webhookMulti = multi
	a.webhookMu.Unlock()
	old.Close()
}

func (a *App) adminWebhookTargets(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	a.webhookMu.RLock()
	specs := append([]webhook.Target(nil), a.webhookTargets...)
	multi := a.webhookMulti
	a.webhookMu.RUnlock()
	c.Header("ETag", webhookTargetsETag(specs))
	c.JSON(http.StatusOK, webhookTargetsView(specs, multi))
}

type webhookTargetUpdate struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	URL            *string  `json:"url"`
	ClearURL       bool     `json:"clear_url"`
	Events         []string `json:"events"`
	Format         string   `json:"format"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	AllowPrivate   bool     `json:"allow_private"`
}

type webhookTargetsUpdateRequest struct {
	Targets       []webhookTargetUpdate `json:"targets"`
	ConfirmDelete bool                  `json:"confirm_delete"`
	ConfirmEmpty  bool                  `json:"confirm_empty_targets"`
}

func newWebhookTargetID() string {
	id, err := webhook.NewTargetID()
	if err != nil {
		return "wh_unavailable"
	}
	return id
}

func (a *App) adminSaveWebhookTargets(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetHeader("If-Match") == "" {
		webhookSettingsError(c, http.StatusPreconditionRequired, "WEBHOOK_PRECONDITION_REQUIRED", "请重新加载 Webhook 配置后保存")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, webhookTargetsBodyLimit+1))
	var req webhookTargetsUpdateRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err != nil || len(raw) > webhookTargetsBodyLimit || dec.Decode(&req) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) {
		webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_INVALID_CONFIG", "Webhook 目标配置格式无效")
		return
	}
	if len(req.Targets) > webhook.MaxTargets {
		webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_TOO_MANY_TARGETS", "最多只能配置 10 个 Webhook 目标")
		return
	}
	a.webhookUpdateMu.Lock()
	defer a.webhookUpdateMu.Unlock()
	if a.webhookClosed || a.rootCtx.Err() != nil {
		webhookSettingsError(c, http.StatusServiceUnavailable, "WEBHOOK_SETTINGS_UNAVAILABLE", "服务正在关闭，未保存配置")
		return
	}
	a.webhookMu.RLock()
	current := append([]webhook.Target(nil), a.webhookTargets...)
	a.webhookMu.RUnlock()
	if c.GetHeader("If-Match") != webhookTargetsETag(current) {
		webhookSettingsError(c, http.StatusPreconditionFailed, "WEBHOOK_CONFIG_CHANGED", "Webhook 配置已被修改；请重新加载后核对，不要覆盖旧配置")
		return
	}
	oldByID := make(map[string]webhook.Target, len(current))
	for _, spec := range current {
		oldByID[spec.ID] = spec
	}
	seen := make(map[string]bool, len(req.Targets))
	next := make([]webhook.Target, 0, len(req.Targets))
	for _, update := range req.Targets {
		id := strings.TrimSpace(update.ID)
		var old webhook.Target
		oldExists := false
		switch {
		case id == "":
			id = newWebhookTargetID()
		case oldByID[id].ID != "":
			old, oldExists = oldByID[id], true
		default:
			webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_UNKNOWN_TARGET", "目标 ID 不存在或不属于当前配置")
			return
		}
		if seen[id] {
			webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_DUPLICATE_TARGET", "目标 ID 重复")
			return
		}
		seen[id] = true
		spec := webhook.Target{ID: id, Name: update.Name, Enabled: update.Enabled,
			Events: append([]string(nil), update.Events...), Format: update.Format,
			TimeoutSeconds: update.TimeoutSeconds, AllowPrivate: update.AllowPrivate}
		// 未提交/空白 URL 表示保留旧秘密；按目标 ID 合并，绝不按数组位置。
		if oldExists {
			spec.URL = old.URL
		}
		if update.ClearURL {
			if update.URL != nil && strings.TrimSpace(*update.URL) != "" {
				webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_INVALID_CONFIG", "清除与替换接收地址不能同时选择")
				return
			}
			spec.URL = ""
		} else if update.URL != nil && strings.TrimSpace(*update.URL) != "" {
			spec.URL = strings.TrimSpace(*update.URL)
		}
		if spec, err = webhook.NormalizeTarget(spec); err != nil {
			webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_INVALID_CONFIG", "目标配置无效：检查名称、事件、地址、编码格式与 1–30 秒超时")
			return
		}
		// 启用删除事件沿用逐目标显式确认；原本已启用并订阅该事件的目标不要求重复确认。
		if spec.Enabled && containsWebhookEvent(spec.Events, webhook.ItemDeleted) &&
			!(oldExists && old.Enabled && containsWebhookEvent(old.Events, webhook.ItemDeleted)) && !req.ConfirmDelete {
			webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_DELETE_CONFIRMATION_REQUIRED", "启用删除通知需明确确认：SmartStrm 可能删除远端整个目录")
			return
		}
		next = append(next, spec)
	}
	if len(next) == 0 && !req.ConfirmEmpty {
		webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_EMPTY_CONFIRMATION_REQUIRED", "清空全部 Webhook 目标需要显式确认")
		return
	}
	dispatcher, err := webhook.NewDispatcher(next, webhook.DefaultEventBudget)
	if err != nil {
		webhookSettingsError(c, http.StatusBadRequest, "WEBHOOK_INVALID_CONFIG", "Webhook 目标配置无效，未保存")
		return
	}
	data, _ := json.Marshal(webhookTargetsConfig{Version: 2, Targets: next})
	if err := a.db.SetSetting(settingWebhookV2, string(data)); err != nil {
		dispatcher.Close()
		webhookSettingsError(c, http.StatusInternalServerError, "WEBHOOK_SETTINGS_UNAVAILABLE", "Webhook 配置保存失败，当前配置未变")
		return
	}
	a.publishWebhook(dispatcher, next, config.WebhookConfig{}, true)
	c.Header("ETag", webhookTargetsETag(next))
	c.JSON(http.StatusOK, webhookTargetsView(next, true))
}

func containsWebhookEvent(events []string, want string) bool {
	return slices.Contains(events, want)
}
