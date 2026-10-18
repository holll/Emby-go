package server

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// setAdminName / currentAdminName 保护 adminName：初始化与登录会写，其它 handler 并发读。
func (a *App) setAdminName(name string) {
	a.adminMu.Lock()
	a.adminName = name
	a.adminMu.Unlock()
}

func (a *App) currentAdminName() string {
	a.adminMu.RLock()
	defer a.adminMu.RUnlock()
	return a.adminName
}

// requestToken 与鉴权及播放 URL 共用同一套凭据提取规则。
func requestToken(c *gin.Context) string {
	if c == nil {
		return ""
	}
	token := c.GetHeader("X-Emby-Token")
	if token == "" {
		token = c.GetHeader("X-MediaBrowser-Token")
	}
	if token == "" {
		token = c.Query("api_key")
	}
	if token == "" {
		token = c.Query("ApiKey")
	}
	if token == "" {
		token = c.Query("X-Emby-Token")
	}
	if token == "" {
		authorization := c.GetHeader("X-Emby-Authorization")
		if authorization == "" {
			authorization = c.GetHeader("Authorization")
		}
		for _, part := range strings.Split(authorization, ",") {
			key, value, found := strings.Cut(strings.TrimSpace(part), "=")
			if found && strings.EqualFold(key, "Token") {
				token = strings.Trim(strings.TrimSpace(value), "\\\"")
				break
			}
		}
	}
	return token
}

// authOK always consults the authoritative store. Never read or populate
// credential positive caches: an in-flight refill could resurrect a revoked key.
// A successful check only authorizes this request, not future requests or streams.
func (a *App) authOK(c *gin.Context) (bool, error) {
	token := requestToken(c)
	if token == "" {
		return false, nil
	}
	// D01：登录会话带过期时间与滑动续期；API key 走独立表、不参与过期。
	valid, err := a.db.ValidateAccessToken(token, time.Now(), a.sessionTTL)
	if err != nil || valid {
		return valid, err
	}
	return a.db.HasAPIKey(token)
}

func (a *App) requireAuth(c *gin.Context) {
	valid, err := a.authOK(c)
	if err != nil {
		// Do not expose DB diagnostics or turn an outage into a logout/401.
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "authentication unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return
	}
	if !valid {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "code": "AUTH_INVALID"})
		return
	}
	c.Next()
}

// logout is protected by requireAuth. API keys retain their existing lifetime:
// only a matching login session is deleted, never the API-key record.
func (a *App) logout(c *gin.Context) {
	if err := a.db.DeleteAccessToken(requestToken(c)); err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "session revocation unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return
	}
	// Exec returns only after the autocommit delete completes successfully.
	c.Status(http.StatusNoContent)
}

// sessionExpiry 计算新会话的过期时间；TTL=0 返回零值（存 NULL，永不过期）。
func (a *App) sessionExpiry(now time.Time) time.Time {
	if a.sessionTTL <= 0 {
		return time.Time{}
	}
	return now.Add(a.sessionTTL)
}

// adminRevokeAllSessions 撤销全部登录会话；调用方当前会话也会失效。
// API 密钥不受影响（D02 已确认其为独立的全权凭据）。
func (a *App) adminRevokeAllSessions(c *gin.Context) {
	revoked, err := a.db.RevokeAllAccessTokens()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "session revocation unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": revoked})
}

// authRateLimited 在 IP 或账号超出失败预算时写 429 并返回 true。
// 429 与 401 分开：客户端能据此退避，而不是把限流误判成密码错误。
func (a *App) authRateLimited(c *gin.Context, username string) bool {
	retry := a.authLimiter.retryAfter(c.ClientIP(), username)
	if retry <= 0 {
		return false
	}
	c.Header("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error": "尝试次数过多，请稍后再试", "code": "AUTH_RATE_LIMITED", "retryable": true,
	})
	return true
}

func (a *App) authStatus(c *gin.Context) {
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	// setup_code_required 只暴露是否配置了引导验证码，不回显内容。
	c.JSON(http.StatusOK, gin.H{"initialized": initialized, "setup_code_required": a.cfg.SetupCode != ""})
}

func validCredentials(username, password string) bool {
	return len([]rune(strings.TrimSpace(username))) >= 3 && len([]rune(password)) >= 9
}

// initializeAllowed 按 initialize_allow_from 限制首次初始化的来源；为空时不限制。
func (a *App) initializeAllowed(c *gin.Context) bool {
	if len(a.initAllow) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(c.ClientIP())
	if err != nil {
		return false
	}
	for _, prefix := range a.initAllow {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (a *App) initialize(c *gin.Context) {
	if !a.initializeAllowed(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "初始化入口不允许该来源", "code": "INIT_SOURCE_DENIED"})
		return
	}
	// 初始化只在空库时可用，按 IP 限流即可；失败一律计数，避免被当成
	// 免费的账号枚举/写库入口。
	if a.authRateLimited(c, "") {
		return
	}
	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Code     string `json:"Code"`
	}
	if c.ShouldBindJSON(&req) != nil || !validCredentials(req.Username, req.Pw) {
		a.authLimiter.recordFailure(c.ClientIP(), "")
		apiError(c, http.StatusBadRequest, "账号至少 3 个字符，密码至少 9 个字符")
		return
	}
	// D04：配置了 setup_code 时，首初始化必须携带；比较用恒定时间实现。
	if a.cfg.SetupCode != "" && !hmac.Equal([]byte(req.Code), []byte(a.cfg.SetupCode)) {
		a.authLimiter.recordFailure(c.ClientIP(), "")
		c.JSON(http.StatusForbidden, gin.H{"error": "初始化验证码不正确", "code": "SETUP_CODE_INVALID"})
		return
	}
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		// 存储故障不是调用方的失败，不计数。
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	if initialized {
		a.authLimiter.recordFailure(c.ClientIP(), "")
		apiError(c, http.StatusConflict, "管理员已初始化")
		return
	}
	if err := a.db.InitializeAdministrator(strings.TrimSpace(req.Username), req.Pw); err != nil {
		a.authLimiter.recordFailure(c.ClientIP(), "")
		apiError(c, http.StatusConflict, "初始化失败，请更换账号后重试")
		return
	}
	a.authLimiter.reset(c.ClientIP(), "")
	a.setAdminName(strings.TrimSpace(req.Username))
	c.Status(http.StatusNoContent)
}

func (a *App) authenticate(c *gin.Context) {
	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
	}
	if err := c.ShouldBind(&req); err != nil {
		a.authLimiter.recordFailure(c.ClientIP(), "")
		apiError(c, http.StatusBadRequest, "invalid credentials payload")
		return
	}
	username := strings.TrimSpace(req.Username)
	if a.authRateLimited(c, username) {
		return
	}
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	if !initialized {
		apiError(c, http.StatusPreconditionRequired, "管理员尚未初始化")
		return
	}
	valid, err := a.db.AuthenticateAdministrator(username, req.Pw)
	if err != nil {
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	if !valid {
		a.authLimiter.recordFailure(c.ClientIP(), username)
		apiError(c, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	a.authLimiter.reset(c.ClientIP(), username)
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		apiError(c, http.StatusInternalServerError, "生成访问令牌失败")
		return
	}
	a.setAdminName(username)
	token := hex.EncodeToString(b)
	if err := a.db.SaveAccessTokenExpiring(token, a.sessionExpiry(time.Now())); err != nil {
		apiError(c, http.StatusInternalServerError, "保存访问令牌失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"AccessToken": token,
		"ServerId":    a.serverID,
		"User":        a.userDto(),
	})
}

// userDto 组装 Emby 的 UserDto。
//
// Policy 不能给空对象：tsukimi 用 serde 严格反序列化，`Policy { IsAdministrator: bool }`
// 是**非 Option** 字段——返回 {} 会让整个登录响应解析失败（登录直接不可用）。
// 这里按真实 Emby 的形状给一份完整策略，本服务是单管理员，故 IsAdministrator 恒为 true。
func (a *App) userDto() gin.H {
	return gin.H{
		"Id": "1", "Name": a.currentAdminName(), "ServerName": a.serverName,
		"ServerId": a.serverID, "HasPassword": true,
		"HasConfiguredPassword": true, "HasConfiguredEasyPassword": false,
		"EnableAutoLogin": false, "LastLoginDate": "", "LastActivityDate": "",
		"Configuration": gin.H{},
		"Policy": gin.H{
			"IsAdministrator": true, "IsHidden": false, "IsDisabled": false,
			"MaxActiveSessions": 0,
			// 播放/下载能力：客户端据此决定是否允许直接播放与转码。
			"EnableMediaPlayback":             true,
			"EnableAudioPlaybackTranscoding":  true,
			"EnableVideoPlaybackTranscoding":  true,
			"EnablePlaybackRemuxing":          true,
			"EnableContentDownloading":        true,
			"EnableSubtitleDownloading":       true,
			"EnableSubtitleManagement":        true,
			"EnableAllFolders":                true,
			"EnableAllChannels":               true,
			"EnableAllDevices":                true,
			"EnableSyncTranscoding":           true,
			"EnableRemoteAccess":              true,
			"EnableRemoteControlOfOtherUsers": false,
			"EnableSharedDeviceControl":       false,
			"EnableLiveTvManagement":          false,
			"EnableLiveTvAccess":              false,
			"EnableContentDeletion":           false,
			"EnablePublicSharing":             false,
			"BlockedChannels":                 []string{},
			"BlockedMediaFolders":             []string{},
			"BlockedTags":                     []string{},
			"AllowedTags":                     []string{},
			"BlockUnratedItems":               []string{},
			"EnabledDevices":                  []string{},
			"EnabledChannels":                 []string{},
			"EnabledFolders":                  []string{},
			"AuthenticationProviderId":        "",
			"PasswordResetProviderId":         "",
		},
	}
}

func (a *App) me(c *gin.Context) {
	c.JSON(http.StatusOK, a.userDto())
}

// userByID 处理 GET /Users/{uid}：tsukimi 登录后立刻调它读 Policy.IsAdministrator。
func (a *App) userByID(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	c.JSON(http.StatusOK, a.userDto())
}

func (a *App) validUser(c *gin.Context) bool {
	if c.Param("uid") != "1" {
		apiError(c, http.StatusNotFound, "user not found")
		return false
	}
	return true
}
