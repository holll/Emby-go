package server

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
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

// embyAuthParams 解析 X-Emby-Authorization（或等价的 Authorization）头里的
// key=value 参数，键名大小写不敏感、值去掉引号。客户端用它声明 Client/
// Device/DeviceId/Version 等身份信息，登录响应要把它们回显进 SessionInfo。
func embyAuthParams(c *gin.Context) map[string]string {
	params := map[string]string{}
	if c == nil {
		return params
	}
	authorization := c.GetHeader("X-Emby-Authorization")
	if authorization == "" {
		authorization = c.GetHeader("Authorization")
	}
	authorization = strings.TrimSpace(authorization)
	if i := strings.IndexAny(authorization, " \t"); i >= 0 &&
		(strings.EqualFold(authorization[:i], "Emby") || strings.EqualFold(authorization[:i], "MediaBrowser")) {
		authorization = strings.TrimSpace(authorization[i+1:])
	}
	for part := range strings.SplitSeq(authorization, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		key = strings.ToLower(key)
		if _, seen := params[key]; seen {
			// 首个出现的值胜出：与凭据提取的历史语义保持一致（重复 Token= 时
			// 用第一个，避免客户端拼接多个头导致后者覆盖前者）。
			continue
		}
		params[key] = strings.Trim(strings.TrimSpace(value), "\\\"")
	}
	return params
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
		// Emby clients may place Token first; embyAuthParams removes only a known
		// scheme before parsing parameters, keeping legacy scheme-less headers.
		token = embyAuthParams(c)["token"]
	}
	return token
}

// authOK always consults the authoritative store. Never read or populate
// credential positive caches: an in-flight refill could resurrect a revoked key.
// A successful check only authorizes this request, not future requests or streams.
func (a *App) authOK(c *gin.Context) (store.User, bool, error) {
	token := requestToken(c)
	if token == "" {
		return store.User{}, false, nil
	}
	// D01：登录会话带过期时间与滑动续期；API key 走独立表、不参与过期。
	userID, valid, err := a.db.ValidateAccessTokenUser(token, time.Now(), a.sessionTTL)
	if err != nil {
		return store.User{}, false, err
	}
	session := valid
	if session {
		c.Set(credentialKindKey, credentialSession)
	} else {
		c.Set(credentialKindKey, credentialAPIKey)
	}
	if !valid {
		userID, valid, err = a.db.APIKeyUser(token)
		if err != nil || !valid {
			return store.User{}, false, err
		}
	}
	u, err := a.db.RequestCredentialUser(token, userID, session)
	if errors.Is(err, store.ErrUserNotFound) {
		// 凭据有效但账户已不存在：按未授权处理，不回落旧口令/旧管理员。
		return store.User{}, false, nil
	}
	if err != nil {
		return store.User{}, false, err
	}
	return u, true, nil
}

func (a *App) requireAuth(c *gin.Context) {
	u, valid, err := a.authOK(c)
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
	if kind, _ := c.Get(credentialKindKey); kind == credentialAPIKey && !apiKeyScopeAllows(c.Request.URL.Path) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "API 密钥仅可用于 Emby 兼容接口", "code": "API_KEY_SCOPE"})
		return
	}
	c.Set(currentUserKey, u)
	c.Next()
}

const currentUserKey = "currentUser"

// 凭据类型：登录会话可访问 Web 管理面，API 密钥只能调 Emby 兼容接口。
const credentialKindKey = "credentialKind"
const (
	credentialSession = "session"
	credentialAPIKey  = "apikey"
)

// apiKeyScopeAllows 判断 API 密钥可用的路径：只放行 Emby 兼容接口（含 /emby 前缀与
// 根路径注册的兼容路由），拒绝全部 /api/*、/healthz 与 Web 管理台。
func apiKeyScopeAllows(path string) bool {
	path = strings.TrimPrefix(strings.ToLower(path), "/emby/")
	path = strings.TrimPrefix(path, "/")
	segment, _, _ := strings.Cut(path, "/")
	switch segment {
	case "items", "users", "videos", "audio", "images", "system", "sessions",
		"branding", "displaypreferences", "search", "genres", "persons", "studios",
		"library", "collections", "playlists", "artists", "shows", "episode", "mediasegments":
		return true
	}
	return false
}

// enforceAPIKeyScope also covers public handlers, which do not run requireAuth.
// Only API keys are rejected here: anonymous requests and session credentials
// retain each handler's existing semantics, including during a store outage.
func (a *App) enforceAPIKeyScope(c *gin.Context) {
	if c.FullPath() != "" && !apiKeyScopeAllows(c.Request.URL.Path) {
		if token := requestToken(c); token != "" {
			_, valid, err := a.db.APIKeyUser(token)
			if err == nil && valid {
				c.Header("Cache-Control", "no-store")
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "API 密钥仅可用于 Emby 兼容接口", "code": "API_KEY_SCOPE"})
				return
			}
		}
	}
	c.Next()
}

// currentUser 返回 requireAuth 解析出的账户；无鉴权上下文时 ok=false。
func currentUser(c *gin.Context) (store.User, bool) {
	v, ok := c.Get(currentUserKey)
	if !ok {
		return store.User{}, false
	}
	u, ok := v.(store.User)
	return u, ok
}

// currentUserID 返回当前账户 id；未鉴权上下文（公开图片等）回退初始管理员。
func (a *App) currentUserID(c *gin.Context) int64 {
	if u, ok := currentUser(c); ok && u.ID > 0 {
		return u.ID
	}
	return 1
}

// requireAdmin 仅允许管理员访问管理接口（挂在 requireAuth 之后）。
func (a *App) requireAdmin(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok || !u.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "需要管理员权限", "code": "ADMIN_REQUIRED"})
		return
	}
	c.Next()
}

// revokeSession 删除当前请求携带的登录会话令牌；返回 false 表示已写错误响应。
// API 密钥保留原有效期：只删匹配的登录会话，从不删 API-key 记录。
// 调用方负责决定成功状态码——自有 /api/auth/* 契约写 204，Emby 兼容端点写 200。
func (a *App) revokeSession(c *gin.Context) bool {
	if err := a.db.DeleteAccessToken(requestToken(c)); err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "session revocation unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return false
	}
	// Exec returns only after the autocommit delete completes successfully.
	if user, ok := currentUser(c); ok {
		a.notifyUserWebhook(c, "user.logout", user)
	}
	return true
}

// logout 是自有 /api/auth/logout：契约固定 204（无响应体）。
func (a *App) logout(c *gin.Context) {
	if !a.revokeSession(c) {
		return
	}
	c.Status(http.StatusNoContent)
}

// embyLogout 是 POST /Sessions/Logout。文档 SessionsService #20 的成功响应写 200，
// 按 200 判定的客户端拿到 204 会走异常分支，因此这里与自有端点分开。
func (a *App) embyLogout(c *gin.Context) {
	if !a.revokeSession(c) {
		return
	}
	c.Status(http.StatusOK)
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
	actor, _ := currentUser(c)
	revoked, err := a.db.AdminRevokeAllAccessTokens(actor)
	if a.adminPermissionError(c, err) {
		return
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "session revocation unavailable", "code": "AUTH_UNAVAILABLE", "retryable": true,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": revoked})
}

// beginCredentialAttempt 在读取请求体和校验密码之前原子消耗入场预算。
// 返回的 ticket 必须由调用方在 defer 中 Finish，否则在途预留不会释放。
func (a *App) beginCredentialAttempt(c *gin.Context) (*authTicket, bool) {
	ticket, retry := a.authGuard.AdmitRequest(c.ClientIP())
	if ticket == nil {
		authRateLimitedRetry(c, retry)
		return nil, false
	}
	return ticket, true
}

// verifyPassword 在 bcrypt 并发槽内校验密码。槽有界等待，超时按限流处理，
// 不能为每个请求起 goroutine 无限排队（那等于把 CPU 攻击换成内存攻击）。
func (a *App) verifyPassword(c *gin.Context, username, password string) (store.User, bool, error, bool) {
	release, ok := a.authGuard.AcquireBcrypt(c.Request.Context(), bcryptSlotWait)
	if !ok {
		return store.User{}, false, nil, false
	}
	defer release()
	user, valid, err := a.db.AuthenticateUser(username, password)
	return user, valid, err, true
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

// validPassword 是创建/修改密码的统一规则：至少 9 个 Unicode 字符，
// 且不超过 bcrypt 的 72 字节上限（超限明确报错，不静默截断）。
func validPassword(password string) bool {
	n := len([]rune(password))
	return n >= 9 && len(password) <= 72
}

func validCredentials(username, password string) bool {
	return len([]rune(strings.TrimSpace(username))) >= 3 && validPassword(password)
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
	// 初始化没有账号维度，只消耗来源/全局预算；所有失败仍计入来源失败历史。
	ticket, ok := a.beginCredentialAttempt(c)
	if !ok {
		return
	}
	ip := c.ClientIP()
	outcome := authOutcomeInterrupted
	defer func() { a.authGuard.Finish(ticket, outcome) }()

	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Code     string `json:"Code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		if credentialBindTooLarge(c, err) {
			return
		}
		outcome = authOutcomeInvalidPassword
		apiError(c, http.StatusBadRequest, "账号至少 3 个字符，密码至少 9 个字符且不超过 72 字节")
		return
	}
	if !validCredentials(req.Username, req.Pw) {
		outcome = authOutcomeInvalidPassword
		apiError(c, http.StatusBadRequest, "账号至少 3 个字符，密码至少 9 个字符且不超过 72 字节")
		return
	}
	// D04：配置了 setup_code 时，首初始化必须携带；比较用恒定时间实现。
	if a.cfg.SetupCode != "" && !hmac.Equal([]byte(req.Code), []byte(a.cfg.SetupCode)) {
		outcome = authOutcomeInvalidPassword
		c.JSON(http.StatusForbidden, gin.H{"error": "初始化验证码不正确", "code": "SETUP_CODE_INVALID"})
		return
	}
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		// 存储故障不是调用方的失败，不计数。
		outcome = authOutcomeStorageFailure
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	if initialized {
		outcome = authOutcomeInvalidPassword
		apiError(c, http.StatusConflict, "管理员已初始化")
		return
	}
	if err := a.db.InitializeAdministrator(strings.TrimSpace(req.Username), req.Pw); err != nil {
		outcome = authOutcomeStorageFailure
		apiError(c, http.StatusConflict, "初始化失败，请更换账号后重试")
		return
	}
	outcome = authOutcomePasswordVerified
	a.authGuard.OnCredentialCommitSuccess(ip, strings.TrimSpace(req.Username))
	a.setAdminName(strings.TrimSpace(req.Username))
	c.Status(http.StatusNoContent)
}

func (a *App) authenticate(c *gin.Context) {
	// 入场预算必须早于 JSON 解码：畸形/巨大请求同样消耗额度。
	ticket, ok := a.beginCredentialAttempt(c)
	if !ok {
		return
	}
	ip := c.ClientIP()
	outcome := authOutcomeInterrupted
	defer func() { a.authGuard.Finish(ticket, outcome) }()

	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
	}
	// Emby 客户端既可能发 JSON 也可能发 form-urlencoded；ShouldBind 按
	// Content-Type 选择绑定器，不能用 ShouldBindJSON 把 form 请求误杀。
	if err := c.ShouldBind(&req); err != nil {
		if credentialBindTooLarge(c, err) {
			return
		}
		outcome = authOutcomeInvalidPassword
		apiError(c, http.StatusBadRequest, "invalid credentials payload")
		return
	}
	username := strings.TrimSpace(req.Username)
	// 解析出账号后原子预留失败额度：已有失败 + 在途预留不能并发穿透阈值。
	if retry := a.authGuard.AttachAccount(ticket, username); retry > 0 {
		authRateLimitedRetry(c, retry)
		return
	}
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		outcome = authOutcomeStorageFailure
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	if !initialized {
		apiError(c, http.StatusPreconditionRequired, "管理员尚未初始化")
		return
	}
	user, valid, err, attempted := a.verifyPassword(c, username, req.Pw)
	if !attempted {
		authRateLimitedRetry(c, time.Second)
		return
	}
	if err != nil {
		outcome = authOutcomeStorageFailure
		apiError(c, http.StatusInternalServerError, "authentication storage unavailable")
		return
	}
	if !valid {
		outcome = authOutcomeInvalidPassword
		if err := a.db.RecordLoginEvent(0, username, ip, false, time.Now()); err != nil {
			slog.Warn("记录失败登录失败", "error", err)
		}
		apiError(c, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		outcome = authOutcomeStorageFailure
		apiError(c, http.StatusInternalServerError, "生成访问令牌失败")
		return
	}
	if user.IsAdmin {
		a.setAdminName(user.Name)
	}
	token := hex.EncodeToString(b)
	if err := a.db.SaveAccessTokenForAuthenticatedUser(token, user, a.sessionExpiry(time.Now())); err != nil {
		// 密码已验证但签发失败：不算密码错误，也不清除任何失败历史。
		outcome = authOutcomeStorageFailure
		if errors.Is(err, store.ErrAuthenticationChanged) {
			apiError(c, http.StatusUnauthorized, "账户凭据已变更，请重新登录")
			return
		}
		apiError(c, http.StatusInternalServerError, "保存访问令牌失败")
		return
	}
	// 只有条件签发真正提交后才清理账号/组合失败历史；IP 喷洒历史保留。
	a.authGuard.OnCredentialCommitSuccess(ip, username)
	outcome = authOutcomePasswordVerified
	if err := a.db.RecordLoginEvent(user.ID, user.Name, c.ClientIP(), true, time.Now()); err != nil {
		slog.Warn("记录登录审计失败", "error", err)
	}
	a.notifyUserWebhook(c, "user.authenticated", user)
	c.JSON(http.StatusOK, gin.H{
		"AccessToken": token,
		"ServerId":    a.serverID,
		"User":        a.userDtoFor(user),
		"SessionInfo": a.sessionInfoFor(c, user),
	})
}

// sessionInfoFor 组装文档 AuthenticationResult 里的 SessionInfo。
// 本服务不维护设备会话表（播放上报只当作事件处理），因此 Id 每次登录新生成：
// 客户端只用它作为「本设备本次会话」的本地标签，不做服务端校验。
// Client/Device/DeviceId/Version 回显客户端在 X-Emby-Authorization 头里声明的值，
// 缺失时给 Emby 自己的默认值，避免客户端读到空串后把设备名显示成空白。
func (a *App) sessionInfoFor(c *gin.Context, u store.User) gin.H {
	params := embyAuthParams(c)
	pick := func(key, fallback string) string {
		if v := params[key]; v != "" {
			return v
		}
		return fallback
	}
	return gin.H{
		"Id":                    randomSessionID(),
		"ServerId":              a.serverID,
		"UserId":                strconv.FormatInt(u.ID, 10),
		"UserName":              u.Name,
		"Client":                pick("client", "Emby"),
		"DeviceName":            pick("device", "Emby-go"),
		"DeviceId":              pick("deviceid", ""),
		"ApplicationVersion":    pick("version", embyVersion),
		"LastActivityDate":      time.Now().UTC().Format(time.RFC3339),
		"SupportsRemoteControl": false,
		"PlayableMediaTypes":    []string{"Video"},
		"SupportedCommands":     []string{},
		"PlayState":             gin.H{},
	}
}

// userDtoFor 按真实账户组装 Emby UserDto。Policy 必须包含 IsAdministrator：
// tsukimi 将它作为非 Option 字段严格反序列化，空策略会导致登录失败。
func (a *App) userDtoFor(u store.User) gin.H {
	return gin.H{
		"Id": strconv.FormatInt(u.ID, 10), "Name": u.Name, "ServerName": a.serverName,
		"ServerId": a.serverID, "HasPassword": true,
		"HasConfiguredPassword": true, "HasConfiguredEasyPassword": false,
		"EnableAutoLogin": false, "LastLoginDate": "", "LastActivityDate": "",
		"Configuration": gin.H{},
		"Policy": gin.H{
			"IsAdministrator": u.IsAdmin, "IsHidden": false, "IsDisabled": false,
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
	u, ok := currentUser(c)
	if !ok {
		apiError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	c.JSON(http.StatusOK, a.userDtoFor(u))
}

// userByID 处理 GET /Users/{uid}：tsukimi 登录后立刻调它读 Policy.IsAdministrator。
func (a *App) userByID(c *gin.Context) {
	u, ok := a.userByIDParam(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, a.userDtoFor(u))
}

func (a *App) userByIDParam(c *gin.Context) (store.User, bool) {
	id, err := strconv.ParseInt(c.Param("uid"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "user not found")
		return store.User{}, false
	}
	u, err := a.db.UserByID(id)
	if errors.Is(err, store.ErrUserNotFound) && id == 1 {
		if name, nameErr := a.db.AdministratorName(); nameErr == nil && name != "" {
			return store.User{ID: 1, Name: name, IsAdmin: true}, true
		}
		if n, countErr := a.db.CountUsers(); countErr == nil && n == 0 {
			return store.User{ID: 1, Name: a.currentAdminName(), IsAdmin: true}, true
		}
	}
	if err != nil {
		apiError(c, http.StatusNotFound, "user not found")
		return store.User{}, false
	}
	if me, ok := currentUser(c); ok && me.ID != u.ID {
		apiError(c, http.StatusNotFound, "user not found")
		return store.User{}, false
	}
	return u, true
}

func (a *App) validUser(c *gin.Context) bool {
	_, ok := a.userByIDParam(c)
	return ok
}

// validQueryUser keeps optional UserId scoped to the authenticated user.
func (a *App) validQueryUser(c *gin.Context) bool {
	raw := c.Query("UserId")
	if raw == "" {
		return true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id != a.currentUserID(c) {
		apiError(c, http.StatusNotFound, "user not found")
		return false
	}
	return true
}
