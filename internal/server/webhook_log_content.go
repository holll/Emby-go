package server

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"emby-go/internal/logging"
	"emby-go/internal/webhook"
)

// webhookLogContent is an allowlisted, bounded view of the notification, not a
// dump of arbitrary payload data. Local media paths are useful for diagnosing
// deletion; remote URLs, session/device identifiers and response bodies are not.
// Construct new values so logging cannot change the event sent to receivers.
func webhookLogContent(event webhook.Event) string {
	content := map[string]any{"Event": webhookLogText(event.Event, 64)}
	if event.Item.Id != "" || event.Item.Name != "" || event.Item.Path != "" {
		item := map[string]any{
			"Id":   webhookLogText(event.Item.Id, 64),
			"Name": webhookLogText(event.Item.Name, 256),
			"Type": webhookLogText(event.Item.Type, 64),
		}
		if event.Item.Path != "" {
			item["Path"] = "[REDACTED: non-local path]"
			if filepath.IsAbs(event.Item.Path) && !strings.Contains(event.Item.Path, "://") {
				item["Path"] = webhookLogText(event.Item.Path, 1024)
			}
		}
		if event.Item.LibraryId != "" {
			item["LibraryId"] = webhookLogText(event.Item.LibraryId, 64)
		}
		if event.Item.RunTimeTicks != 0 {
			item["RunTimeTicks"] = event.Item.RunTimeTicks
		}
		if data := webhookLogUserData(event.Item.UserData); len(data) > 0 {
			item["UserData"] = data
		}
		content["Item"] = item
	}
	if event.User != nil {
		content["User"] = webhook.User{Id: webhookLogText(event.User.Id, 64), Name: webhookLogText(event.User.Name, 128)}
	}
	if event.Server.Id != "" || event.Server.Name != "" {
		content["Server"] = webhook.Server{Id: webhookLogText(event.Server.Id, 64), Name: webhookLogText(event.Server.Name, 128)}
	}
	if !event.Timestamp.IsZero() {
		content["Timestamp"] = event.Timestamp.UTC().Format(time.RFC3339Nano)
		// Sender.Send mirrors Timestamp into Date on its own event copy.
		content["Date"] = content["Timestamp"]
	}
	if event.PlaybackInfo != nil {
		content["PlaybackInfo"] = map[string]any{
			"PositionTicks":   event.PlaybackInfo.PositionTicks,
			"PositionSeconds": float64(event.PlaybackInfo.PositionTicks) / 10000000,
			"IsPaused":        event.PlaybackInfo.IsPaused,
		}
	} else if event.Session != nil {
		content["PlayState"] = map[string]any{
			"PositionTicks":   event.Session.PlayState.PositionTicks,
			"PositionSeconds": float64(event.Session.PlayState.PositionTicks) / 10000000,
			"IsPaused":        event.Session.PlayState.IsPaused,
		}
	}
	// Only finite numbers, strings, bools and maps constructed above reach here.
	data, err := json.Marshal(content)
	if err != nil {
		return `{"ContentUnavailable":true}`
	}
	return string(data)
}

func webhookLogText(value string, limit int) string {
	value = logging.SanitizeText(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…[truncated]"
	}
	return value
}

func webhookLogUserData(data map[string]any) map[string]any {
	out := make(map[string]any)
	for _, key := range []string{"Played", "IsFavorite", "Likes"} {
		if value, ok := data[key].(bool); ok {
			out[key] = value
		}
	}
	for _, key := range []string{"PlaybackPositionTicks", "PlayCount", "PlayedPercentage"} {
		switch value := data[key].(type) {
		case int, int64:
			out[key] = value
		case float64:
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				out[key] = value
			}
		}
	}
	if value, ok := data["LastPlayedDate"].(string); ok {
		if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
			out["LastPlayedDate"] = value
		}
	}
	return out
}

func webhookEventDescription(event string) string {
	switch event {
	case webhook.ItemDeleted:
		return "删除影片"
	case webhook.ItemUpdated:
		return "更新影片"
	case webhook.ItemRate:
		return "收藏或评分变更"
	case webhook.ItemUnrate:
		return "清除评分"
	case webhook.ItemMarkPlayed:
		return "标为已看"
	case webhook.ItemMarkUnplayed:
		return "取消已看"
	case webhook.PlaybackStart:
		return "开始播放"
	case webhook.PlaybackProgress:
		return "播放进度"
	case webhook.PlaybackStop:
		return "停止播放"
	case webhook.PlaybackPause:
		return "暂停播放"
	case webhook.PlaybackUnpause:
		return "继续播放"
	case webhook.UserAuthenticated:
		return "登录成功"
	case webhook.UserLogout:
		return "退出登录"
	default:
		return "未知事件"
	}
}
