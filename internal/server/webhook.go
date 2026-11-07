package server

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
	"emby-go/internal/webhook"
)

// Non-destructive events never share the deletion path or synthesize a remote
// path. Notification failure must not change an already committed API result.
func (a *App) notifyWebhook(c *gin.Context, event webhook.Event) {
	sender := a.webhookSender()
	if sender == nil || !sender.Subscribed(event.Event) {
		c.Header("X-Webhook-Status", "disabled")
		return
	}
	event.Server = webhook.Server{Id: a.serverID, Name: a.serverName}
	event.Timestamp = time.Now().UTC()
	started := time.Now()
	result := sender.Send(a.rootCtx, event)
	c.Header("X-Webhook-Status", result.Status)
	a.recordWebhookDeliveries(event, result)
	logWebhookDelivery(event, result, time.Since(started))
}

// Include the essential diagnostics in msg because the disk log viewer does not
// expose structured attributes. Frequent progress acknowledgments stay at DEBUG;
// failures always remain WARN. Acknowledgment does not prove remote side effects.
func logWebhookDelivery(event webhook.Event, result webhook.Result, elapsed time.Duration) {
	sent, failed, skipped := webhookResultCounts(result)
	level := slog.LevelInfo
	if event.Event == webhook.PlaybackProgress || result.Status == webhook.ResultDisabled {
		level = slog.LevelDebug
	}
	ackLevel := level
	if result.Status == webhook.ResultFailed || result.Status == webhook.ResultPartial {
		level = slog.LevelWarn
	}
	content := webhookLogContent(event)
	description := webhookEventDescription(event.Event)
	message := fmt.Sprintf("Webhook 投递结果：event=%s item_id=%s status=%s targets=%d sent=%d failed=%d skipped_budget=%d duration_ms=%d；通知=%s；内容（脱敏摘要）=%s",
		event.Event, event.Item.Id, result.Status, len(result.Targets), sent, failed, skipped, elapsed.Milliseconds(), description, content)
	slog.Log(nil, level, message, "event", event.Event, "item_id", event.Item.Id,
		"status", result.Status, "targets", len(result.Targets), "sent", sent,
		"failed", failed, "skipped_budget", skipped, "duration_ms", elapsed.Milliseconds(),
		"notification", description, "notification_content", content)
	for _, outcome := range result.Targets {
		if outcome.Status != webhook.TargetSent {
			continue
		}
		message := fmt.Sprintf("Webhook 接收端已确认：event=%s item_id=%s target_id=%s status=%s；仅表示接收确认，不代表远端业务执行完成",
			event.Event, event.Item.Id, outcome.ID, outcome.Status)
		slog.Log(nil, ackLevel, message, "event", event.Event, "item_id", event.Item.Id,
			"target_id", outcome.ID, "status", outcome.Status)
	}
	logWebhookTargetFailures(event, result)
}

// Put safe diagnostic fields in the message as well: the disk log viewer only
// exposes msg, not arbitrary structured attributes. Never log target names,
// URLs, payload paths, response bodies or raw transport errors here.
func logWebhookTargetFailures(event webhook.Event, result webhook.Result) {
	for _, outcome := range result.Targets {
		if outcome.Status != webhook.TargetFailed && outcome.Status != webhook.TargetSkippedBudget {
			continue
		}
		reason := webhookFailureReason(outcome.ErrorCode)
		message := fmt.Sprintf("Webhook 投递失败：event=%s item_id=%s target_id=%s status=%s error_code=%s；%s",
			event.Event, event.Item.Id, outcome.ID, outcome.Status, outcome.ErrorCode, reason)
		if event.Event == webhook.ItemDeleted {
			message += "；索引已删除，远端结果未知，请人工核对，不要重复删除或自动重发"
		}
		slog.Warn(message, "event", event.Event, "item_id", event.Item.Id,
			"target_id", outcome.ID, "status", outcome.Status, "error_code", outcome.ErrorCode)
	}
}

func webhookFailureReason(code string) string {
	if strings.HasPrefix(code, "status_") {
		if status, err := strconv.Atoi(strings.TrimPrefix(code, "status_")); err == nil && status >= 100 && status <= 599 {
			return fmt.Sprintf("接收端返回 HTTP %d，请检查接收端日志和配置", status)
		}
	}
	switch code {
	case "timeout":
		return "等待接收端响应超时，远端可能已执行"
	case "canceled":
		return "投递上下文已取消（例如服务关闭）"
	case "ignored":
		return "接收端明确忽略通知，请检查接收端事件开关"
	case "read_response":
		return "读取接收端确认失败，远端可能已执行"
	case "response_too_large":
		return "接收端确认响应过大，无法确认结果"
	case "invalid_event":
		return "事件参数不符合投递要求"
	case "budget_exhausted":
		return "总投递预算耗尽，该目标未发送"
	default:
		return "请求失败，请检查网络、DNS、TLS及出站地址安全限制"
	}
}

func webhookResultCounts(result webhook.Result) (sent, failed, skipped int) {
	for _, outcome := range result.Targets {
		switch outcome.Status {
		case webhook.TargetSent:
			sent++
		case webhook.TargetFailed:
			failed++
		case webhook.TargetSkippedBudget:
			skipped++
		}
	}
	return sent, failed, skipped
}

func (a *App) movieWebhook(c *gin.Context, name string, movie store.Movie) webhook.Event {
	event := webhook.Event{Event: name, Item: webhook.Item{
		Id: strconv.FormatInt(movie.ID, 10), Name: store.CleanDisplayName(movie.Title),
		Path: movie.SourcePath, Type: "Movie", LibraryId: strconv.FormatInt(movie.LibraryID, 10),
	}}
	if movie.RuntimeSeconds > 0 && movie.RuntimeSeconds <= (1<<63-1)/10000000 {
		event.Item.RunTimeTicks = movie.RuntimeSeconds * 10000000
	}
	if user, ok := currentUser(c); ok {
		event.User = &webhook.User{Id: strconv.FormatInt(user.ID, 10), Name: user.Name}
	}
	return event
}

func (a *App) notifyUserWebhook(c *gin.Context, name string, user store.User) {
	a.notifyWebhook(c, webhook.Event{Event: name, User: &webhook.User{Id: strconv.FormatInt(user.ID, 10), Name: user.Name}})
}

func (a *App) notifyMovieDataWebhook(c *gin.Context, name string, movie store.Movie, data map[string]any) {
	event := a.movieWebhook(c, name, movie)
	event.Item.UserData = data
	a.notifyWebhook(c, event)
}

// Re-read only when subscribed, after indexing succeeded. Failure to collect a
// payload never turns a saved edit into a failed write or sends a stale title.
func (a *App) notifyIndexedUpdate(c *gin.Context, id int64) {
	sender := a.webhookSender()
	if sender == nil || !sender.Subscribed("item.updated") {
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		c.Header("X-Webhook-Status", "failed")
		slog.Warn("Webhook update payload unavailable", "item_id", id)
		return
	}
	a.notifyWebhook(c, a.movieWebhook(c, "item.updated", movie))
}

func (a *App) notifyPlaybackWebhook(c *gin.Context, movie store.Movie, kind store.PlaybackKind, eventName, sessionID, deviceID string, ticks int64, paused bool) {
	name := "playback.progress"
	switch kind {
	case store.PlaybackStart:
		name = "playback.start"
	case store.PlaybackStopped:
		name = "playback.stop"
	case store.PlaybackProgress:
		switch strings.ToLower(eventName) {
		case "pause":
			name, paused = "playback.pause", true
		case "unpause":
			name, paused = "playback.unpause", false
		}
	}
	event := a.movieWebhook(c, name, movie)
	event.Session = &webhook.Session{Id: sessionID, DeviceId: deviceID,
		PlayState: webhook.PlayState{PositionTicks: ticks, IsPaused: paused}}
	event.PlaybackInfo = &webhook.PlaybackInfo{PositionTicks: ticks, PlaySessionId: sessionID, IsPaused: paused}
	a.notifyWebhook(c, event)
}

// notifyMovieDeleted runs only for an explicit administrator item deletion,
// AFTER its database transaction commits. Scans and library deletion must never
// call this: receivers such as SmartStrm delete an entire remote directory.
// Delivery is bounded, single-attempt and best effort, not a durable outbox.
// A failure cannot undo the local commit and must not turn it into HTTP 500.
func (a *App) notifyMovieDeleted(c *gin.Context, movie store.Movie) {
	sender := a.webhookSender()
	if sender == nil || !sender.Subscribed(webhook.ItemDeleted) {
		c.Header("X-Webhook-Status", "disabled")
		return
	}
	// Do not turn arbitrary indexed URLs or directory paths into delete commands.
	if !filepath.IsAbs(movie.SourcePath) || !strings.EqualFold(filepath.Ext(movie.SourcePath), ".strm") {
		c.Header("X-Webhook-Status", "skipped")
		slog.Warn("Webhook deletion event skipped: invalid source path", "item_id", movie.ID)
		return
	}
	event := webhook.Event{
		Event: webhook.ItemDeleted,
		Item: webhook.Item{
			Id: strconv.FormatInt(movie.ID, 10), Name: store.CleanDisplayName(movie.Title),
			Path: movie.SourcePath, Type: "Movie",
		},
		Server:    webhook.Server{Id: a.serverID, Name: a.serverName},
		Timestamp: time.Now().UTC(),
	}
	// Do not use Request.Context: a disconnect after the committed deletion must
	// not itself suppress delivery. App shutdown still cancels this request.
	started := time.Now()
	result := sender.Send(a.rootCtx, event)
	c.Header("X-Webhook-Status", result.Status)
	logWebhookDelivery(event, result, time.Since(started))
}
