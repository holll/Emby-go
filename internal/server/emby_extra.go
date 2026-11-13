package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"emby-go/internal/store"

	"github.com/gin-gonic/gin"
)

// 本文件补齐官方 RestAPI 文档里「本项目结构上不会有数据」的端点。
// 这些端点返回空集合而不是 404：客户端进入对应页面/Tab 时显示「暂无」
// 而不是弹错误，且不改变任何已有行为。
// 依据：/root/src/EmbyRestApiDocs 的 LibraryService / UserLibraryService /
// TvShowsService / PlaystateService，逐条见 API-CONTRACT §4.21。

// emptyThemeMedia 返回文档 ThemeMediaResult 的空形状。
func (a *App) emptyThemeMedia(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"OwnerId":          0,
		"Items":            []gin.H{},
		"TotalRecordCount": 0,
	})
}

// emptyThemeMediaAll 返回文档 AllThemeMediaResult 的三段空形状。
// 主题曲/主题视频/原声带在本项目里都没有对应资源（NFO 不携带，扫描也不落库）。
func (a *App) emptyThemeMediaAll(c *gin.Context) {
	empty := func() gin.H {
		return gin.H{"OwnerId": 0, "Items": []gin.H{}, "TotalRecordCount": 0}
	}
	c.JSON(http.StatusOK, gin.H{
		"ThemeVideosResult":     empty(),
		"ThemeSongsResult":      empty(),
		"SoundtrackSongsResult": empty(),
	})
}

// sessions 对应文档 GET /Sessions（SessionInfo[]）。
// 本项目只记录播放会话（playback_sessions），没有独立的客户端长连接会话，
// 因此这里返回「正在播放的会话」，字段按 SessionInfo 的核心子集给出。
func (a *App) sessions(c *gin.Context) {
	if !a.validQueryUser(c) {
		return
	}
	active, err := a.db.ActivePlaybackSessions()
	if err != nil {
		itemDataUnavailable(c)
		return
	}
	viewerID := a.currentUserID(c)
	_, admin, ok := a.viewerLibraries(c)
	if !ok {
		itemDataUnavailable(c)
		return
	}
	out := make([]gin.H, 0, len(active))
	for _, s := range active {
		if !admin && s.UserID != viewerID {
			continue
		}
		user, err := a.db.UserByID(s.UserID)
		if err != nil {
			continue
		}
		item := gin.H{
			"Id":                    s.SessionKey,
			"ServerId":              a.serverID,
			"UserId":                strconv.FormatInt(s.UserID, 10),
			"UserName":              user.Name,
			"DeviceId":              s.DeviceID,
			"DeviceName":            "Emby-go",
			"Client":                "Emby",
			"ApplicationVersion":    embyVersion,
			"LastActivityDate":      s.StartedAt,
			"SupportsRemoteControl": false,
			"PlayableMediaTypes":    []string{"Video"},
			"SupportedCommands":     []string{},
			"PlayState": gin.H{
				"PositionTicks": s.PositionTicks,
				"CanSeek":       true,
				"IsPaused":      false,
			},
		}
		if movie, err := a.db.Movie(s.MovieID); err == nil && movie.IsVisible() {
			if a.canViewMovie(c, movie) {
				data, _ := a.db.DataUser(s.UserID, s.MovieID)
				item["NowPlayingItem"] = a.embyItemActors(movie, data, nil, false)
			} else {
				continue
			}
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// apiKeys 对应文档 GET /Auth/Keys（管理员）。
// 文档把成功响应写成「二进制内容」（模板缺陷），实际是密钥列表；
// 这里按 Emby 的 AuthenticationInfo 子集返回 Id/AppName/CreatedDate。
func (a *App) apiKeys(c *gin.Context) {
	if !a.embyAdminOnly(c) {
		return
	}
	keys, err := a.db.APIKeys()
	if err != nil {
		apiError(c, http.StatusInternalServerError, "api keys unavailable")
		return
	}
	out := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		out = append(out, gin.H{
			"Id": k.ID, "AppName": k.Name, "CreatedDate": k.CreatedAt,
			"DeviceId": "", "DeviceName": "", "AppVersion": "",
		})
	}
	c.JSON(http.StatusOK, out)
}

// createAPIKey 对应文档 POST /Auth/Keys?App=...（管理员，成功 200 无返回内容）。
func (a *App) createAPIKey(c *gin.Context) {
	if !a.embyAdminOnly(c) {
		return
	}
	app := strings.TrimSpace(c.Query("App"))
	if app == "" {
		apiError(c, http.StatusBadRequest, "App required")
		return
	}
	user, ok := currentUser(c)
	if !ok {
		apiError(c, http.StatusForbidden, "administrator required")
		return
	}
	if _, err := a.db.AdminCreateAPIKey(user, app); err != nil {
		apiError(c, http.StatusInternalServerError, "api key creation failed")
		return
	}
	c.Status(http.StatusOK)
}

// deleteAPIKey 对应文档 DELETE /Auth/Keys/{Key} 与 POST /Auth/Keys/{Key}/Delete。
func (a *App) deleteAPIKey(c *gin.Context) {
	if !a.embyAdminOnly(c) {
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		apiError(c, http.StatusBadRequest, "key required")
		return
	}
	user, ok := currentUser(c)
	if !ok {
		apiError(c, http.StatusForbidden, "administrator required")
		return
	}
	if err := a.db.AdminDeleteAPIKey(user, key, 0); err != nil {
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			apiError(c, http.StatusNotFound, "api key not found")
			return
		}
		apiError(c, http.StatusInternalServerError, "api key deletion failed")
		return
	}
	c.Status(http.StatusOK)
}

// embyAdminOnly 在 Emby 兼容路由上做管理员判定。
// embyRoute.auth 只有「是否需登录」两态，管理员限制只能在 handler 里判，
// 语义与 /api/admin 的 requireAdmin 一致（403 ADMIN_REQUIRED）。
func (a *App) embyAdminOnly(c *gin.Context) bool {
	if u, ok := currentUser(c); ok && u.IsAdmin {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "administrator required", "code": "ADMIN_REQUIRED"})
	return false
}

// years 对应文档 GET /Years（Emby_Api_UserLibrary_TagItem[]）。
// 文档模型只列了 Id 字段，这里按 Id + Name 给出（多给 Name 无害且更有用）。
func (a *App) years(c *gin.Context) { a.scalarEntityList(c, "Year") }

// officialRatings 对应文档 GET /OfficialRatings（OfficialRatingItem[]）。
func (a *App) officialRatings(c *gin.Context) { a.scalarEntityList(c, "OfficialRating") }

func (a *App) scalarEntityList(c *gin.Context, kind string) {
	if !a.validQueryUser(c) {
		return
	}
	libraryID, collection, ok := parseParentScope(c.Query("ParentId"))
	if !ok {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}
	visible, _, viewerOK := a.viewerLibraries(c)
	if !viewerOK {
		itemDataUnavailable(c)
		return
	}
	names, err := a.db.EntityScalarNamesVisible(kind, libraryID, visible, collection)
	if err != nil {
		itemDataUnavailable(c)
		return
	}
	if kind == "Year" {
		// 年份按数值倒序，客户端下拉框按此顺序展示。
		slices.SortFunc(names, func(x, y string) int { return strings.Compare(y, x) })
	}
	out := make([]gin.H, 0, len(names))
	for _, name := range names {
		out = append(out, gin.H{"Id": entityId(kind, name), "Name": name})
	}
	c.JSON(http.StatusOK, out)
}
