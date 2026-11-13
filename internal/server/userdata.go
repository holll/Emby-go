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

// userDataDTO 统一 UserData 输出形状，按文档的 UserItemDataDto 补齐字段：
// Rating 是 0–10 的数值（文档要求 double），Likes 是 3.x 的布尔语义；
// 两者并存——第三方客户端读 Rating，自有前端读 Likes。
// UnplayedItemCount 对单部影片恒为 0（它表达「剧集里还有几集没看」）。
func userDataDTO(d store.UserData) gin.H {
	h := gin.H{
		"PlaybackPositionTicks": d.PositionTicks,
		"PlayCount":             d.PlayCount,
		"Played":                d.Played,
		"IsFavorite":            d.IsFavorite,
		"PlayedPercentage":      d.PlayedPercentage,
		"LastPlayedDate":        d.LastPlayedAt,
		"Rating":                ratingFromLikes(d.Likes),
		"UnplayedItemCount":     0,
	}
	if d.Likes != 0 {
		h["Likes"] = d.Likes > 0
	}
	return h
}

// ratingFromLikes 把内部的赞/踩三态映射到文档的 0–10 数值：
// 赞=10、未评=0、踩=1（1 而非 0，因为 0 在 Emby 里表示「未评分」，
// 用 1 才能让客户端把「已踩」与「没评过」区分开）。
func ratingFromLikes(likes int) float64 {
	switch {
	case likes > 0:
		return 10
	case likes < 0:
		return 1
	default:
		return 0
	}
}

func (a *App) userDataJSON(id int64, d store.UserData) gin.H {
	h := userDataDTO(d)
	// Key 在文档里是 UserItemDataDto 的稳定标识，随 ItemId 一起给出。
	itemID := strconv.FormatInt(id, 10)
	h["ItemId"] = itemID
	h["Key"] = itemID
	h["ServerId"] = a.serverID
	return h
}

func userDataWriteError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	apiError(c, http.StatusInternalServerError, "user data write failed")
}

func (a *App) favoriteItem(c *gin.Context, favorite bool) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	data, err := a.db.SetIndexedFavoriteDataForUser(a.currentUserID(c), id, favorite)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	a.notifyMovieDataWebhook(c, "item.rate", movie, userDataDTO(data))
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
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	data, err := a.db.SetIndexedLikesDataForUser(a.currentUserID(c), id, likes)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	name := "item.rate"
	if likes == 0 {
		name = "item.unrate"
	}
	a.notifyMovieDataWebhook(c, name, movie, userDataDTO(data))
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
		apiError(c, http.StatusBadRequest, "Likes required (true|false)")
	}
}

func (a *App) unrate(c *gin.Context) { a.rateItem(c, 0) }

// itemUserData 处理 /Users/{uid}/Items/{id}/UserData 的读取与写入。
// 文档只列了 POST（成功 200、无返回内容），但第三方客户端普遍也读 GET，
// 两条都提供：GET 返回 UserItemDataDto，POST 应用 body 里我们支持的字段。
func (a *App) itemUserData(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	userID := a.currentUserID(c)
	if c.Request.Method == http.MethodGet {
		data, err := a.db.DataUser(userID, id)
		if err != nil {
			userDataWriteError(c, err)
			return
		}
		c.JSON(http.StatusOK, a.userDataJSON(id, data))
		return
	}
	var body struct {
		Played     *bool    `json:"Played"`
		IsFavorite *bool    `json:"IsFavorite"`
		Rating     *float64 `json:"Rating"`
		Likes      *bool    `json:"Likes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		apiError(c, http.StatusBadRequest, "invalid user data")
		return
	}
	// 逐字段套用；每个 setter 都在同一事务里重读快照，最后整体返回。
	data, err := a.db.DataUser(userID, id)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	if body.Played != nil && *body.Played != data.Played {
		if data, err = a.db.SetIndexedPlayedDataForUser(userID, id, *body.Played); err != nil {
			userDataWriteError(c, err)
			return
		}
	}
	if body.IsFavorite != nil && *body.IsFavorite != data.IsFavorite {
		if data, err = a.db.SetIndexedFavoriteDataForUser(userID, id, *body.IsFavorite); err != nil {
			userDataWriteError(c, err)
			return
		}
	}
	// Likes（3.x 布尔）优先于 Rating（4.x 数值）；两者都不给就不动评分。
	likes := 0
	applyLikes := false
	switch {
	case body.Likes != nil:
		applyLikes = true
		if *body.Likes {
			likes = 1
		} else {
			likes = -1
		}
	case body.Rating != nil:
		applyLikes = true
		likes = likesFromRating(*body.Rating)
	}
	if applyLikes && likes != data.Likes {
		if data, err = a.db.SetIndexedLikesDataForUser(userID, id, likes); err != nil {
			userDataWriteError(c, err)
			return
		}
	}
	c.Status(http.StatusOK)
}

// likesFromRating 把文档的 0–10 数值映射回内部三态：>5 视为赞、>0 视为踩、0 未评。
// 与 ratingFromLikes 互为逆映射（10↔赞、1↔踩、0↔未评）。
func likesFromRating(rating float64) int {
	switch {
	case rating > 5:
		return 1
	case rating > 0:
		return -1
	default:
		return 0
	}
}

func (a *App) hideFromResume(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	hide := strings.EqualFold(c.Query("Hide"), "true")
	data, err := a.db.SetIndexedHideFromResumeDataForUser(a.currentUserID(c), id, hide)
	if err != nil {
		userDataWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, a.userDataJSON(id, data))
}
