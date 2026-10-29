package server

import (
	"context"
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
	return a.admitAdminOperationContext(c, nil)
}

// admitAdminOperationContext is the bounded form. Handlers that hold a
// cross-request mutex pass their request deadline so a cancelled request stops
// waiting for the SQLite writer instead of blocking every other administrator.
func (a *App) admitAdminOperationContext(c *gin.Context, ctx context.Context) bool {
	actor, _ := currentUser(c)
	var err error
	if ctx == nil {
		err = a.db.AdmitAdminOperation(actor)
	} else {
		err = a.db.AdmitAdminOperationContext(ctx, actor)
	}
	if a.adminPermissionError(c, err) {
		return false
	}
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authorization timed out", "code": "AUTH_UNAVAILABLE", "retryable": true})
			return false
		}
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authorization unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true})
		return false
	}
	return true
}
