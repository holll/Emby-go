package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (a *App) adminLibrarySprites(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(c, 400, "invalid library id")
		return
	}
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil || len(raw) != 1 || raw["interval_seconds"] == nil {
		apiError(c, 400, "provide only interval_seconds (10 to 86400)")
		return
	}
	var req struct {
		Interval int `json:"interval_seconds"`
	}
	if json.Unmarshal(data, &req) != nil || req.Interval < 10 || req.Interval > 86400 {
		apiError(c, 400, "interval_seconds must be an integer from 10 to 86400")
		return
	}
	// Revalidate the current actor after consuming a potentially slow body.
	if !a.admitAdminOperation(c) {
		return
	}
	if err = a.db.SetLibrarySpriteInterval(c.Request.Context(), id, req.Interval); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			apiError(c, 404, "library not found")
		} else {
			apiError(c, 500, "library preview setting could not be saved")
		}
		return
	}
	c.JSON(200, gin.H{"interval_seconds": req.Interval})
}
