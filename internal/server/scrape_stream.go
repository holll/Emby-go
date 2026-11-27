package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
)

// notifyScrapeLocked wakes every reader without queuing intermediate snapshots.
// The next reader installs a fresh channel under the same lock as its snapshot.
func (a *App) notifyScrapeLocked() {
	if a.scrapeChanged != nil {
		close(a.scrapeChanged)
		a.scrapeChanged = nil
	}
}

func (a *App) adminScrapeStream(c *gin.Context) {
	response := http.NewResponseController(c.Writer)
	// Same write bound as the log stream: a stalled client must not prevent shutdown.
	if err := response.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		apiError(c, http.StatusServiceUnavailable, "streaming unavailable")
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	send := func(frame string) bool {
		if err := response.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return false
		}
		_, err := fmt.Fprint(c.Writer, frame)
		if err != nil || response.Flush() != nil {
			return false
		}
		// The deadline bounds a write, not the idle time between events.
		return response.SetWriteDeadline(time.Time{}) == nil
	}

	// Check authoritative access even when the worker has no progress to report.
	// A fixed cadence avoids a DB lookup for each in-memory progress update.
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	for {
		a.scrapeTaskMu.Lock()
		status := a.scrapeStatus
		status.Failures = slices.Clone(status.Failures)
		if status.Running && a.scrapeChanged == nil {
			a.scrapeChanged = make(chan struct{})
		}
		changed := a.scrapeChanged
		a.scrapeTaskMu.Unlock()
		data, err := json.Marshal(status)
		if err != nil || !send(fmt.Sprintf("event: progress\ndata: %s\n\n", data)) || !status.Running {
			return
		}
	wait:
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-a.rootCtx.Done():
				return
			case <-heartbeat.C:
				user, valid, err := a.authOK(c)
				credential, _ := c.Get(credentialKindKey)
				var code string
				switch {
				case err != nil:
					code = "AUTH_UNAVAILABLE"
				case !valid:
					code = "AUTH_INVALID"
				case credential != credentialSession:
					code = "API_KEY_SCOPE"
				case !user.IsAdmin:
					code = "ADMIN_REQUIRED"
				}
				if code != "" {
					send(fmt.Sprintf("event: error\ndata: {\"code\":%q}\n\n", code))
					return
				}
				if !send(": heartbeat\n\n") {
					return
				}
			case <-changed:
				break wait
			}
		}
	}
}
