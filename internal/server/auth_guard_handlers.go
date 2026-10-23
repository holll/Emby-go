package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
)

// credentialLimited 在读取登录/改密/轮转请求体之前施加远小于普通 API 的
// 8 KiB 预算。凭据请求都是几十字节的 JSON；先限长再解析，避免畸形大包
// 进入 JSON/form 解码器。
func (a *App) credentialLimited(h gin.HandlerFunc) gin.HandlerFunc {
	limit := a.authGuard.cfg.CredentialBodyBytes
	return func(c *gin.Context) {
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "credentials payload too large", "code": "AUTH_BODY_TOO_LARGE",
			})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		h(c)
	}
}

// credentialBindTooLarge 把 MaxBytesReader 触发的超限错误映射为 413；否则
// chunked 请求可以绕过 Content-Length 预检，最终只得到含糊的 400。
func credentialBindTooLarge(c *gin.Context, err error) bool {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": "credentials payload too large", "code": "AUTH_BODY_TOO_LARGE",
		})
		return true
	}
	return false
}

// adminAuthLimits 返回有效策略、聚合计数与并发槽占用；不返回任何攻击者
// 可控的用户名列表，也不返回可以拿来枚举账号的明细。
func (a *App) adminAuthLimits(c *gin.Context) {
	c.JSON(http.StatusOK, a.authGuard.Snapshot())
}

// requireInteractiveSession 要求请求携带的是登录会话而不是长期 API key。
// 改密、清限流、密钥轮转这类再认证操作不能被一个长期凭据静默代劳。
func (a *App) requireInteractiveSession(c *gin.Context) (store.User, bool) {
	token := requestToken(c)
	if token == "" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要交互登录会话", "code": "AUTH_SESSION_REQUIRED"})
		return store.User{}, false
	}
	userID, valid, err := a.db.ValidateAccessTokenUser(token, time.Now(), a.sessionTTL)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "authentication unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return store.User{}, false
	}
	if !valid {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要交互登录会话", "code": "AUTH_SESSION_REQUIRED"})
		return store.User{}, false
	}
	user, err := a.db.UserByID(userID)
	if err != nil || !user.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要交互登录会话", "code": "AUTH_SESSION_REQUIRED"})
		return store.User{}, false
	}
	return user, true
}

// adminUnlockAuth 是受保护的应急恢复入口。它要求交互会话 + 当前密码再认证，
// 只清除指定账号/来源的失败历史，不清请求桶、不清全局拒绝计数。
func (a *App) adminUnlockAuth(c *gin.Context) {
	admin, ok := a.requireInteractiveSession(c)
	if !ok {
		return
	}
	var req struct {
		TargetType      string `json:"target_type"`
		Value           string `json:"value"`
		Reason          string `json:"reason"`
		CurrentPassword string `json:"current_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	targetType := strings.ToLower(strings.TrimSpace(req.TargetType))
	value := strings.TrimSpace(req.Value)
	reason := strings.TrimSpace(req.Reason)
	if targetType != "account" && targetType != "ip" {
		apiError(c, http.StatusBadRequest, "target_type 只支持 account 或 ip")
		return
	}
	if value == "" || utf8.RuneCountInString(value) > 256 {
		apiError(c, http.StatusBadRequest, "target 不合法")
		return
	}
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		apiError(c, http.StatusBadRequest, "必须填写原因（最多 200 字）")
		return
	}
	if targetType == "ip" {
		if _, err := netip.ParseAddr(value); err != nil {
			apiError(c, http.StatusBadRequest, "来源地址不合法")
			return
		}
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
	authenticated, valid, err, attempted := a.verifyPassword(c, admin.Name, req.CurrentPassword)
	if !attempted {
		authRateLimitedRetry(c, time.Second)
		return
	}
	if err != nil {
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
	if !a.admitAdminOperation(c) {
		return
	}
	if !a.authGuard.Unlock(targetType, value) {
		apiError(c, http.StatusBadRequest, "target 不合法")
		return
	}
	// 再认证成功本身是凭据提交，清理管理员账号/组合的失败历史；不清来源喷洒历史。
	a.authGuard.OnCredentialCommitSuccess(ip, admin.Name)
	outcome = authOutcomePasswordVerified

	auditTarget := value
	if targetType == "account" {
		auditTarget = authAccountDigest(value)
	}
	slog.Warn("管理员解锁认证失败历史", "admin", safeAuthLogValue(admin.Name),
		"target_type", targetType, "target", safeAuthLogValue(auditTarget), "reason", safeAuthLogValue(reason))
	c.JSON(http.StatusOK, gin.H{"unlocked": true})
}

// errAuthGuardTarget 保留给后续密钥轮转复用同一目标校验语义。
var errAuthGuardTarget = errors.New("invalid auth guard target")
