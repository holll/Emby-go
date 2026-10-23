package server

import (
	"errors"
	"net/http"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

func (a *App) adminPermissionError(c *gin.Context, err error) bool {
	if !errors.Is(err, store.ErrAdminGenerationStale) {
		return false
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "管理员凭据已变更", "code": "ADMIN_REQUIRED"})
	return true
}

// admitAdminOperation linearizes admission, not completion. The short store
// transaction ends before workers, network calls or filesystem writes begin.
func (a *App) admitAdminOperation(c *gin.Context) bool {
	actor, _ := currentUser(c)
	err := a.db.AdmitAdminOperation(actor)
	if a.adminPermissionError(c, err) {
		return false
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authorization unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true})
		return false
	}
	return true
}
