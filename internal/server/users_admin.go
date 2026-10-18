package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"unicode/utf8"

	"emby-go/internal/store"
)

// userView 是管理端返回的用户形状：不包含密码哈希，附媒体库授权与最近登录。
type userView struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	IsAdmin     bool    `json:"is_admin"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt string  `json:"last_login_at"`
	LibraryIDs  []int64 `json:"library_ids"`
}

func (a *App) userViews(users []store.User) ([]userView, error) {
	last, err := a.db.UserLastLogin()
	if err != nil {
		return nil, err
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		libs, err := a.db.UserLibraryIDs(u.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, userView{ID: u.ID, Name: u.Name, IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt, LastLoginAt: last[u.ID], LibraryIDs: libs})
	}
	return out, nil
}

func normalizeName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, utf8.RuneCountInString(name) >= 3
}

// validateLibraryIDs 确认每个库都真实存在，避免写入悬空授权。
func (a *App) validateLibraryIDs(ids []int64) ([]int64, bool) {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if id <= 0 {
			return nil, false
		}
		if _, err := a.db.Library(id); err != nil {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// adminListUsers 返回全部账户及其媒体库授权（需管理员）。
func (a *App) adminListUsers(c *gin.Context) {
	users, err := a.db.ListUsers()
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取用户失败")
		return
	}
	views, err := a.userViews(users)
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取用户失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": views, "total": len(views)})
}

// adminCreateUser 新建账户并显式分配媒体库；默认不分配任何库。
func (a *App) adminCreateUser(c *gin.Context) {
	var req struct {
		Name       string  `json:"name"`
		Password   string  `json:"password"`
		IsAdmin    bool    `json:"is_admin"`
		LibraryIDs []int64 `json:"library_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	name, ok := normalizeName(req.Name)
	if !ok || len(req.Password) < 9 {
		apiError(c, http.StatusBadRequest, "用户名至少 3 个字符，密码至少 9 个字符")
		return
	}
	libs, ok := a.validateLibraryIDs(req.LibraryIDs)
	if !ok {
		apiError(c, http.StatusBadRequest, "媒体库不存在")
		return
	}
	user, err := a.db.CreateUser(name, req.Password, req.IsAdmin)
	if err != nil {
		status := http.StatusBadRequest
		apiError(c, status, userErrorText(err))
		return
	}
	if err := a.db.SetUserLibraries(user.ID, libs); err != nil {
		apiError(c, http.StatusInternalServerError, "保存媒体库授权失败")
		return
	}
	views, err := a.userViews([]store.User{user})
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取用户失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": views[0]})
}

func userErrorText(err error) string {
	switch err {
	case store.ErrUserExists:
		return "用户名已存在"
	case store.ErrLastAdmin:
		return "至少需要保留一个管理员"
	case store.ErrProtectedUser:
		return "初始管理员不可删除或降级"
	default:
		return "用户操作失败"
	}
}

// adminUpdateUser 局部更新账户：用户名、密码、管理员标记、媒体库授权。
func (a *App) adminUpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusBadRequest, "用户 id 无效")
		return
	}
	if _, err := a.db.UserByID(id); err != nil {
		apiError(c, http.StatusNotFound, "用户不存在")
		return
	}
	var req struct {
		Name       *string  `json:"name"`
		Password   *string  `json:"password"`
		IsAdmin    *bool    `json:"is_admin"`
		LibraryIDs *[]int64 `json:"library_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Name != nil {
		name, ok := normalizeName(*req.Name)
		if !ok {
			apiError(c, http.StatusBadRequest, "用户名至少 3 个字符")
			return
		}
		if err := a.db.RenameUser(id, name); err != nil {
			apiError(c, http.StatusBadRequest, userErrorText(err))
			return
		}
	}
	if req.Password != nil {
		if len(*req.Password) < 9 {
			apiError(c, http.StatusBadRequest, "密码至少 9 个字符")
			return
		}
		if err := a.db.SetUserPassword(id, *req.Password); err != nil {
			apiError(c, http.StatusBadRequest, userErrorText(err))
			return
		}
		if err := a.db.RevokeUserAccessTokens(id); err != nil {
			apiError(c, http.StatusInternalServerError, "撤销旧会话失败")
			return
		}
	}
	if req.IsAdmin != nil {
		if err := a.db.SetUserAdmin(id, *req.IsAdmin); err != nil {
			apiError(c, http.StatusBadRequest, userErrorText(err))
			return
		}
	}
	if req.LibraryIDs != nil {
		libs, ok := a.validateLibraryIDs(*req.LibraryIDs)
		if !ok {
			apiError(c, http.StatusBadRequest, "媒体库不存在")
			return
		}
		if err := a.db.SetUserLibraries(id, libs); err != nil {
			apiError(c, http.StatusInternalServerError, "保存媒体库授权失败")
			return
		}
	}
	user, err := a.db.UserByID(id)
	if err != nil {
		apiError(c, http.StatusNotFound, "用户不存在")
		return
	}
	views, err := a.userViews([]store.User{user})
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取用户失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": views[0]})
}

// adminSetUserLibraries 覆盖式设置用户的媒体库可见范围。
func (a *App) adminSetUserLibraries(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusBadRequest, "用户 id 无效")
		return
	}
	if _, err := a.db.UserByID(id); err != nil {
		apiError(c, http.StatusNotFound, "用户不存在")
		return
	}
	var req struct {
		LibraryIDs []int64 `json:"library_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	libs, ok := a.validateLibraryIDs(req.LibraryIDs)
	if !ok {
		apiError(c, http.StatusBadRequest, "媒体库不存在")
		return
	}
	if err := a.db.SetUserLibraries(id, libs); err != nil {
		apiError(c, http.StatusInternalServerError, "保存媒体库授权失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"library_ids": libs})
}

// adminDeleteUser 删除账户；播放/登录审计保留，个人播放数据随账户删除。
func (a *App) adminDeleteUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusBadRequest, "用户 id 无效")
		return
	}
	if id == a.currentUserID(c) {
		apiError(c, http.StatusBadRequest, "不能删除当前登录账户")
		return
	}
	if err := a.db.DeleteUser(id); err != nil {
		if store.NotFound(err) || err == store.ErrUserNotFound {
			apiError(c, http.StatusNotFound, "用户不存在")
			return
		}
		apiError(c, http.StatusBadRequest, userErrorText(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// changePassword 允许用户修改自己的密码；成功后撤销该账户全部会话，要求重新登录。
func (a *App) changePassword(c *gin.Context) {
	user, err := a.db.UserByID(a.currentUserID(c))
	if err != nil {
		apiError(c, http.StatusUnauthorized, "账户不存在")
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	if _, ok, err := a.db.AuthenticateUser(user.Name, req.CurrentPassword); err != nil || !ok {
		apiError(c, http.StatusBadRequest, "当前密码不正确")
		return
	}
	if len(req.NewPassword) < 9 {
		apiError(c, http.StatusBadRequest, "密码至少 9 个字符")
		return
	}
	if err := a.db.SetUserPassword(user.ID, req.NewPassword); err != nil {
		apiError(c, http.StatusInternalServerError, "保存密码失败")
		return
	}
	if err := a.db.RevokeUserAccessTokens(user.ID); err != nil {
		apiError(c, http.StatusInternalServerError, "撤销旧会话失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// adminLoginEvents 返回登录审计；user_id 可选，0/缺省表示全部用户。
func (a *App) adminLoginEvents(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := a.db.LoginEvents(userID, limit)
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取登录记录失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "total": len(events)})
}

// adminPlaybackEvents 返回播放活动记录（含影片与用户快照）；user_id 可选。
func (a *App) adminPlaybackEvents(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := a.db.RecentPlaybackEvents(userID, limit)
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取播放记录失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "total": len(events)})
}
