package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
)

// parseAPIKeyIfMatch 解析 `"ak-<stableID>-<version>"`。稳定 ID 内部为 32 位
// 十六进制，接口前缀 `ak-` 只用于区分摘要；版本必须是正整数。
func parseAPIKeyIfMatch(value string) (stable string, version int64, err error) {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	if value == "" || !strings.HasPrefix(value, "ak-") {
		return "", 0, store.ErrAPIKeyPrecondition
	}
	rest := strings.TrimPrefix(value, "ak-")
	idx := strings.LastIndex(rest, "-")
	if idx <= 0 || idx == len(rest)-1 {
		return "", 0, store.ErrAPIKeyPrecondition
	}
	stable = strings.TrimSpace(rest[:idx])
	n, parseErr := strconv.ParseInt(rest[idx+1:], 10, 64)
	if parseErr != nil || n <= 0 || stable == "" {
		return "", 0, store.ErrAPIKeyPrecondition
	}
	if !isAPIKeyStableID(stable) {
		return "", 0, store.ErrAPIKeyPrecondition
	}
	return stable, n, nil
}

// adminRotateAPIKey 原子轮转单把密钥：交互会话 + 当前密码再认证 + If-Match
// 版本前置条件，同一事务替换摘要并递增版本。旧 key 在提交后立即失效。
func (a *App) adminRotateAPIKey(c *gin.Context) {
	admin, ok := a.requireInteractiveSession(c)
	if !ok {
		return
	}
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))
	if ifMatch == "" {
		c.AbortWithStatusJSON(http.StatusPreconditionRequired, gin.H{
			"error": "缺少 If-Match 版本前置条件", "code": "API_KEY_PRECONDITION_REQUIRED",
		})
		return
	}
	stable, expectedVersion, err := parseAPIKeyIfMatch(ifMatch)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": "If-Match 格式不合法", "code": "API_KEY_PRECONDITION_INVALID",
		})
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		Reason          string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		if credentialBindTooLarge(c, err) {
			return
		}
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		apiError(c, http.StatusBadRequest, "必须填写轮转原因（最多 200 字）")
		return
	}
	if req.CurrentPassword == "" || len(req.CurrentPassword) > 72 {
		apiError(c, http.StatusBadRequest, "需要当前密码再认证")
		return
	}
	ticket, ok := a.beginCredentialAttempt(c)
	if !ok {
		return
	}
	ip := c.ClientIP()
	outcome := authOutcomeInterrupted
	defer func() { a.authGuard.Finish(ticket, outcome) }()
	if retry := a.authGuard.AttachAccount(ticket, admin.Name); retry > 0 {
		authRateLimitedRetry(c, retry)
		return
	}
	authenticated, valid, authErr, attempted := a.verifyPassword(c, admin.Name, req.CurrentPassword)
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
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "当前密码不正确", "code": "AUTH_REVERIFY_FAILED"})
		return
	}
	if authenticated.ID != admin.ID {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "账户凭据已变更，请重新登录", "code": "AUTH_INVALID"})
		return
	}
	// 再认证成功本身是凭据提交；清账号/组合失败历史，不清来源喷洒历史。
	a.authGuard.OnCredentialCommitSuccess(ip, admin.Name)
	outcome = authOutcomePasswordVerified

	pathStable, pathErr := a.db.ResolveAPIKeyStableID(c.Param("key"))
	if pathErr != nil || pathStable != stable {
		c.AbortWithStatusJSON(http.StatusPreconditionFailed, gin.H{
			"error": "If-Match 与路径中的密钥不一致", "code": "API_KEY_VERSION_STALE",
		})
		return
	}
	rotated, err := a.db.RotateAPIKey(authenticated, stable, expectedVersion)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrAPIKeyNotFound):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "密钥不存在", "code": "API_KEY_NOT_FOUND"})
		return
	case errors.Is(err, store.ErrAPIKeyVersionStale):
		c.AbortWithStatusJSON(http.StatusPreconditionFailed, gin.H{"error": "密钥版本已变化，请刷新后重试", "code": "API_KEY_VERSION_STALE"})
		return
	case errors.Is(err, store.ErrAdminGenerationStale):
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "账户凭据已变更，请重新登录", "code": "AUTH_INVALID"})
		return
	default:
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "密钥轮转暂时不可用", "code": "API_KEY_UNAVAILABLE", "retryable": true,
		})
		return
	}
	slog.Info("轮转 API 密钥", "id", rotated.ID, "version", rotated.Version, "reason", safeAuthLogValue(reason))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, rotated)
}

// isAPIKeyStableID 校验内部稳定 ID：32 位小写十六进制。
func isAPIKeyStableID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
