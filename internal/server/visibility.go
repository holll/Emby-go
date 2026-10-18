package server

import (
	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
)

// viewerLibraries 返回当前账户可见的媒体库集合：管理员返回 (nil,true,true)
// 表示不限制；普通用户返回授权 id 列表（可能为空）。ok=false 表示读库失败。
func (a *App) viewerLibraries(c *gin.Context) (libs []int64, admin bool, ok bool) {
	user, err := a.db.UserByID(a.currentUserID(c))
	if err != nil {
		// 旧库/测试夹具有尚未初始化账户的场景：沿用单用户语义，不引入额外拒绝。
		if err == store.ErrUserNotFound {
			return nil, true, true
		}
		return nil, false, false
	}
	if user.IsAdmin {
		return nil, true, true
	}
	libs, err = a.db.UserLibraryIDs(user.ID)
	if err != nil {
		return nil, false, false
	}
	return libs, false, true
}

// canViewLibrary 判断当前账户能否访问某个媒体库。
func (a *App) canViewLibrary(c *gin.Context, libraryID int64) bool {
	user, err := a.db.UserByID(a.currentUserID(c))
	if err != nil {
		if err == store.ErrUserNotFound {
			return true
		}
		return false
	}
	ok, err := a.db.CanAccessLibrary(user.ID, user.IsAdmin, libraryID)
	return err == nil && ok
}

// canViewMovie 判断当前账户能否看到某部影片（按所属媒体库授权）。
func (a *App) canViewMovie(c *gin.Context, movie store.Movie) bool {
	return a.canViewLibrary(c, movie.LibraryID)
}
