package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// auditAdminAction 记录管理端写操作（POST/PUT/PATCH/DELETE），GET 不记，
// 避免把日常查看淹没在审计里；记录失败只打日志，不影响响应。
func (a *App) auditAdminAction(c *gin.Context) {
	c.Next()
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return
	}
	u, ok := currentUser(c)
	if !ok {
		return
	}
	status := c.Writer.Status()
	// 只记录路由模板与非秘密对象 ID：原始 URL 可能含查询凭据，或把
	// DELETE /apikeys/:key 里的长期密钥写进审计与错误日志。
	path := c.FullPath()
	if path == "" {
		path = "[unmatched admin route]"
	}
	if err := a.db.RecordAdminAudit(u.ID, u.Name, c.Request.Method, path, status, time.Now()); err != nil {
		slog.Warn("记录管理操作审计失败", "path", path, "error", err)
	}
}

// adminAuditEvents 只读查询最近的写操作审计（管理员）。
func (a *App) adminAuditEvents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	events, err := a.db.AdminAuditEvents(limit)
	if err != nil {
		apiError(c, http.StatusInternalServerError, "审计读取失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}
