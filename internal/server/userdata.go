package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"emby-go/internal/store"

	"github.com/gin-gonic/gin"
)

// userDataJSON 组装 UserItemDataDto 形态的响应。
func (a *App) userDataJSON(id int64, d store.UserData) gin.H {
	return gin.H{
		"ItemId":                strconv.FormatInt(id, 10),
		"ServerId":              a.serverID,
		"PlaybackPositionTicks": d.PositionTicks,
		"PlayCount":             d.PlayCount,
		"Played":                d.Played,
		"IsFavorite":            d.IsFavorite,
		"PlayedPercentage":      d.PlayedPercentage,
		"LastPlayedDate":        d.LastPlayedAt,
	}
}

func userDataWriteError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "user data write failed"})
}

func (a *App) favoriteItem(c *gin.Context, favorite bool) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	data, err := a.db.SetIndexedFavoriteData(id, favorite)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, a.userDataJSON(id, data))
}

func (a *App) favorite(c *gin.Context)   { a.favoriteItem(c, true) }
func (a *App) unfavorite(c *gin.Context) { a.favoriteItem(c, false) }

func (a *App) rateItem(c *gin.Context, likes int) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	data, err := a.db.SetIndexedLikesData(id, likes)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, a.userDataJSON(id, data))
}

// rating 处理 POST /Users/{uid}/Items/{id}/Rating?Likes=true|false。
func (a *App) rating(c *gin.Context) {
	switch strings.ToLower(c.Query("Likes")) {
	case "true":
		a.rateItem(c, 1)
	case "false":
		a.rateItem(c, -1)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Likes required (true|false)"})
	}
}

func (a *App) unrate(c *gin.Context) { a.rateItem(c, 0) }

func (a *App) hideFromResume(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	hide := strings.EqualFold(c.Query("Hide"), "true")
	data, err := a.db.SetIndexedHideFromResumeData(id, hide)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, a.userDataJSON(id, data))
}
