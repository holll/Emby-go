package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

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
	if !ok || !validPassword(req.Password) {
		apiError(c, http.StatusBadRequest, "用户名至少 3 个字符，密码至少 9 个字符且不超过 72 字节")
		return
	}
	libs, ok := a.validateLibraryIDs(req.LibraryIDs)
	if !ok {
		apiError(c, http.StatusBadRequest, "媒体库不存在")
		return
	}
	user, err := a.db.AdminCreateUser(adminUserActorID(c), name, req.Password, req.IsAdmin, libs, adminUserActor(c))
	if err != nil {
		adminUserErrorResponse(c, err)
		return
	}
	views, err := a.userViews([]store.User{user})
	if err != nil {
		apiError(c, http.StatusInternalServerError, "读取用户失败")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": views[0]})
}

// Missing authentication must never use the legacy currentUserID owner fallback.
func adminUserActor(c *gin.Context) store.User {
	u, _ := currentUser(c)
	return u
}

func adminUserActorID(c *gin.Context) int64 {
	user, ok := currentUser(c)
	if !ok {
		return 0
	}
	return user.ID
}

func adminUserErrorResponse(c *gin.Context, err error) {
	if err == store.ErrAdminGenerationStale {
		apiError(c, http.StatusForbidden, "管理员权限已撤销")
		return
	}
	apiError(c, http.StatusBadRequest, userErrorText(err))
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
		req.Name = &name
	}
	if req.Password != nil && !validPassword(*req.Password) {
		apiError(c, http.StatusBadRequest, "密码至少 9 个字符且不超过 72 字节")
		return
	}
	if req.LibraryIDs != nil {
		libs, ok := a.validateLibraryIDs(*req.LibraryIDs)
		if !ok {
			apiError(c, http.StatusBadRequest, "媒体库不存在")
			return
		}
		req.LibraryIDs = &libs
	}
	if err := a.db.AdminUpdateUser(adminUserActorID(c), id, store.AdminUserUpdate{
		Name: req.Name, Password: req.Password, IsAdmin: req.IsAdmin, LibraryIDs: req.LibraryIDs,
	}, adminUserActor(c)); err != nil {
		adminUserErrorResponse(c, err)
		return
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
	if err := a.db.AdminUpdateUser(adminUserActorID(c), id, store.AdminUserUpdate{LibraryIDs: &libs}, adminUserActor(c)); err != nil {
		adminUserErrorResponse(c, err)
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
	if err := a.db.AdminDeleteUser(adminUserActorID(c), id, adminUserActor(c)); err != nil {
		if store.NotFound(err) || err == store.ErrUserNotFound {
			apiError(c, http.StatusNotFound, "用户不存在")
			return
		}
		adminUserErrorResponse(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// changePassword 允许用户修改自己的密码；成功后撤销该账户全部会话，要求重新登录。
// 当前密码校验与登录共用同一 guard：改密不是绕开防爆破的第二入口。
func (a *App) changePassword(c *gin.Context) {
	ticket, ok := a.beginCredentialAttempt(c)
	if !ok {
		return
	}
	ip := c.ClientIP()
	outcome := authOutcomeInterrupted
	defer func() { a.authGuard.Finish(ticket, outcome) }()

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
		if credentialBindTooLarge(c, err) {
			return
		}
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	if retry := a.authGuard.AttachAccount(ticket, user.Name); retry > 0 {
		authRateLimitedRetry(c, retry)
		return
	}
	authenticated, valid, authErr, attempted := a.verifyPassword(c, user.Name, req.CurrentPassword)
	if !attempted {
		authRateLimitedRetry(c, time.Second)
		return
	}
	if authErr != nil {
		outcome = authOutcomeStorageFailure
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "authentication unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return
	}
	if !valid {
		outcome = authOutcomeInvalidPassword
		apiError(c, http.StatusBadRequest, "当前密码不正确")
		return
	}
	// Names are mutable: another account can take this name between the ID
	// lookup above and password authentication. Never apply its proof to this
	// request, even when both accounts happen to share a password.
	if authenticated.ID != user.ID {
		apiError(c, http.StatusUnauthorized, "账户凭据已变更，请重新登录")
		return
	}
	if !validPassword(req.NewPassword) {
		apiError(c, http.StatusBadRequest, "密码至少 9 个字符且不超过 72 字节")
		return
	}
	if err := a.db.SetAuthenticatedUserPassword(authenticated, req.NewPassword); err != nil {
		outcome = authOutcomeStorageFailure
		if err == store.ErrAuthenticationChanged {
			apiError(c, http.StatusUnauthorized, "账户凭据已变更，请重新登录")
			return
		}
		apiError(c, http.StatusInternalServerError, "保存密码失败")
		return
	}
	outcome = authOutcomePasswordVerified
	a.authGuard.OnCredentialCommitSuccess(ip, user.Name)
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
