package server

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
)

// viewerLibraries 返回当前账户可见的媒体库集合：管理员返回 (nil,true,true)
// 表示不限制；普通用户返回授权 id 列表（可能为空）。ok=false 表示读库失败。
func (a *App) viewerLibraries(c *gin.Context) (libs []int64, admin bool, ok bool) {
	authenticated, present := currentUser(c)
	if !present || authenticated.ID <= 0 {
		return nil, false, false
	}
	user, err := a.db.UserByID(authenticated.ID)
	if err != nil {
		return nil, false, false
	}
	if user.IsAdmin {
		return nil, true, true
	}
	libs, err = a.db.UserLibraryIDs(user.ID)
	if err != nil {
		return nil, false, false
	}
	if libs == nil {
		libs = []int64{}
	}
	return libs, false, true
}

// canViewLibrary 判断当前账户能否访问某个媒体库。
func (a *App) canViewLibrary(c *gin.Context, libraryID int64) bool {
	authenticated, present := currentUser(c)
	if !present || authenticated.ID <= 0 {
		return false
	}
	user, err := a.db.UserByID(authenticated.ID)
	if err != nil {
		return false
	}
	ok, err := a.db.CanAccessLibrary(user.ID, user.IsAdmin, libraryID)
	return err == nil && ok
}

// canViewMovie 判断当前账户能否看到某部影片（按所属媒体库授权）。
func (a *App) canViewMovie(c *gin.Context, movie store.Movie) bool {
	return a.canViewLibrary(c, movie.LibraryID)
}

// viewerCacheKey 返回缓存键里的账户授权段：管理员固定 "all"，普通用户为已授权
// 媒体库 id 列表（可能为空串）。与 viewerLibraries 口径一致。
func (a *App) viewerCacheKey(c *gin.Context) (string, bool) {
	libs, admin, ok := a.viewerLibraries(c)
	if !ok {
		return "", false
	}
	return libraryScopeCacheKey(libs, admin), true
}

// libraryScopeCacheKey uses the same grant snapshot as the associated query.
func libraryScopeCacheKey(libs []int64, admin bool) string {
	if admin {
		return "all"
	}
	parts := make([]string, len(libs))
	for i, id := range libs {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// visibleEntity resolves authenticated metadata only from granted libraries.
// Anonymous image-byte routes deliberately use their existing global lookup.
func (a *App) visibleEntity(c *gin.Context, kind, name string) (poster string, exists, ok bool) {
	visible, _, ok := a.viewerLibraries(c)
	if !ok {
		itemDataUnavailable(c)
		return "", false, false
	}
	names, err := a.db.EntityNamesVisible(kind, 0, visible, "")
	if err != nil {
		itemDataUnavailable(c)
		return "", false, false
	}
	for _, candidate := range names {
		if candidate != name {
			continue
		}
		poster, err := a.db.EntityPosterVisible(kind, name, 0, visible, "")
		if err != nil {
			itemDataUnavailable(c)
			return "", false, false
		}
		return poster, true, true
	}
	return "", false, true
}
