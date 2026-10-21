package server

import (
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
	if err := sender.Send(a.rootCtx, event); err != nil {
		c.Header("X-Webhook-Status", "failed")
		slog.Warn("Webhook delivery failed", "event", event.Event)
		return
	}
	c.Header("X-Webhook-Status", "sent")
}

func (a *App) movieWebhook(c *gin.Context, name string, movie store.Movie) webhook.Event {
	event := webhook.Event{Event: name, Item: webhook.Item{
		Id: strconv.FormatInt(movie.ID, 10), Name: movie.Title,
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
			Id: strconv.FormatInt(movie.ID, 10), Name: movie.Title,
			Path: movie.SourcePath, Type: "Movie",
		},
		Server:    webhook.Server{Id: a.serverID, Name: a.serverName},
		Timestamp: time.Now().UTC(),
	}
	// Do not use Request.Context: a disconnect after the committed deletion must
	// not itself suppress delivery. App shutdown still cancels this request.
	if err := sender.Send(a.rootCtx, event); err != nil {
		c.Header("X-Webhook-Status", "failed")
		slog.Warn("Webhook delivery failed after item deletion", "event", webhook.ItemDeleted, "item_id", movie.ID)
		return
	}
	c.Header("X-Webhook-Status", "sent")
	slog.Info("Webhook receiver acknowledged item deletion", "event", webhook.ItemDeleted, "item_id", movie.ID)
}
