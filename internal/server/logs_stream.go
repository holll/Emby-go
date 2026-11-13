package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"emby-go/internal/logging"
	"github.com/gin-gonic/gin"
)

// adminLogsStream exposes raw disk text only on the authenticated admin group.
// The old /logs/disk handler remains the separately redacted compatibility API.
func (a *App) adminLogsStream(c *gin.Context) {
	kind := c.DefaultQuery("kind", "app")
	if kind != "app" && kind != "request" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log kind", "code": "LOGS_KIND_INVALID"})
		return
	}
	after := c.Query("after")
	page, err := logging.ReadRawLogs(kind, after)
	if err != nil {
		status, code := http.StatusServiceUnavailable, "LOGS_UNAVAILABLE"
		if errors.Is(err, logging.ErrLogsCursorInvalid) {
			status, code = http.StatusBadRequest, "LOGS_CURSOR_INVALID"
		}
		c.JSON(status, gin.H{"error": "log stream unavailable", "code": code})
		return
	}
	controller := http.NewResponseController(c.Writer)
	// A slow/disconnected reader can otherwise block a write beyond the shared
	// 15-second HTTP shutdown budget. Each batch/heartbeat has a five-second cap.
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "streaming unavailable", "code": "LOGS_UNAVAILABLE"})
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	send := func(event string, value any, id string) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return false
		}
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if id != "" {
			if _, err := fmt.Fprintf(c.Writer, "id: %s\n", id); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	first := true
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-a.rootCtx.Done():
			return
		default:
		}
		// A successful handshake does not authorize a stream forever. Recheck the
		// authoritative session and role before each disk batch, including idle ticks.
		user, valid, authErr := a.authOK(c)
		code := ""
		switch {
		case authErr != nil:
			code = "AUTH_UNAVAILABLE"
		case !valid:
			code = "AUTH_INVALID"
		case !user.IsAdmin:
			code = "ADMIN_REQUIRED"
		default:
			if credential, _ := c.Get(credentialKindKey); credential != credentialSession {
				code = "API_KEY_SCOPE"
			}
		}
		if code != "" {
			send("error", gin.H{"code": code}, "")
			return
		}
		if first || page.Text != "" || page.Reset || page.Cursor != after {
			if !send("logs", page, page.Cursor) {
				return
			}
			after = page.Cursor
			first = false
		}
		// Drain bounded catch-up batches without waiting a second between them.
		if !page.HasMore {
			select {
			case <-c.Request.Context().Done():
				return
			case <-a.rootCtx.Done():
				return
			case <-ticker.C:
			case <-heartbeat.C:
				if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
					return
				}
				if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
					return
				}
				if controller.Flush() != nil {
					return
				}
			}
		}
		page, err = logging.ReadRawLogs(kind, after)
		if err != nil {
			send("error", gin.H{"code": "LOGS_UNAVAILABLE"}, "")
			return
		}
	}
}
