package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// userDataJSON 组装 UserItemDataDto 形态的响应。
func (a *App) userDataJSON(id int64) (gin.H, bool) {
	m, err := a.db.Movie(id)
	if err != nil || !m.IsVisible() {
		return nil, false
	}
	d, _ := a.db.Data(id)
	percentage := float64(0)
	if m.RuntimeSeconds > 0 && d.PositionTicks > 0 {
		percentage = float64(d.PositionTicks) / float64(m.RuntimeSeconds*10000000) * 100
	}
	return gin.H{
		"ItemId":                strconv.FormatInt(id, 10),
		"ServerId":              a.serverID,
		"PlaybackPositionTicks": d.PositionTicks,
		"PlayCount":             d.PlayCount,
		"Played":                d.Played,
		"IsFavorite":            d.IsFavorite,
		"PlayedPercentage":      percentage,
		"LastPlayedDate":        d.LastPlayedAt,
	}, true
}

// userDataWrite 在用户数据写后统一 bump 版本并清缓存（与播放/续播路径一致）。
func (a *App) userDataWrite(libraryID int64) {
	_ = a.db.BumpVersion(libraryID)
	a.cache.Clear()
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
	if err := a.db.SetFavorite(id, favorite); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.userDataWrite(movie.LibraryID)
	resp, ok := a.userDataJSON(id)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, resp)
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
	if err := a.db.SetLikes(id, likes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.userDataWrite(movie.LibraryID)
	resp, ok := a.userDataJSON(id)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, resp)
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
	if err := a.db.SetHideFromResume(id, hide); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.userDataWrite(movie.LibraryID)
	resp, ok := a.userDataJSON(id)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, resp)
}
