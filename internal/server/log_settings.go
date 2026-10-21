package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"emby-go/internal/logging"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

const settingLogRetention = "logging.retention_days"

func loadLogRetention(db *store.Store) (int, error) {
	settings, err := db.Settings()
	if err != nil {
		return 0, errors.New("log retention settings unavailable")
	}
	raw, ok := settings[settingLogRetention]
	if !ok {
		return 0, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 || days > logging.MaxRetentionDays {
		return 0, errors.New("invalid log retention settings")
	}
	return days, nil
}

func logRetentionETag(days int) string { return fmt.Sprintf("\"lr1-%d\"", days) }

func (a *App) adminLogSettings(c *gin.Context) {
	a.logRetentionMu.Lock()
	defer a.logRetentionMu.Unlock()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"retention_days": a.logRetentionDays, "max_days": logging.MaxRetentionDays, "config_etag": logRetentionETag(a.logRetentionDays)})
}

func (a *App) adminSaveLogSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !a.ready.Load() {
		c.JSON(503, gin.H{"error": "log settings unavailable"})
		return
	}
	var req struct {
		Days    *int `json:"retention_days"`
		Confirm bool `json:"confirm_cleanup"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Days == nil || *req.Days < 0 || *req.Days > logging.MaxRetentionDays {
		c.JSON(400, gin.H{"error": "invalid log retention settings"})
		return
	}
	if *req.Days > 0 && !req.Confirm {
		c.JSON(400, gin.H{"code": "LOG_CLEANUP_CONFIRMATION_REQUIRED", "error": "confirm irreversible history cleanup"})
		return
	}
	tag := c.GetHeader("If-Match")
	if tag == "" {
		c.JSON(428, gin.H{"error": "log settings precondition required"})
		return
	}
	a.logRetentionMu.Lock()
	defer a.logRetentionMu.Unlock()
	if tag != logRetentionETag(a.logRetentionDays) {
		c.JSON(412, gin.H{"code": "LOG_SETTINGS_CHANGED", "error": "log settings changed; reload before saving"})
		return
	}
	if err := a.db.SetSetting(settingLogRetention, strconv.Itoa(*req.Days)); err != nil {
		c.JSON(500, gin.H{"error": "log settings save failed"})
		return
	}
	a.logRetentionDays = *req.Days
	// A purge is irreversible: commit policy first; report partial cleanup without
	// claiming the saved setting rolled back. The writer will retry next log day.
	cleanupErr := a.applyLogRetention(*req.Days)
	c.JSON(200, gin.H{"retention_days": *req.Days, "max_days": logging.MaxRetentionDays, "config_etag": logRetentionETag(*req.Days), "cleanup_warning": cleanupErr != nil})
}
