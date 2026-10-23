package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// 认证入口的本地资源预算。这里保护的是三件不同的事：
//  1. 请求入场（IP/全局令牌桶）：畸形请求、账号枚举、并发洪水；
//  2. 失败尝试（IP/账号/组合固定窗口）：猜密次数；
//  3. bcrypt 并发槽：CPU 与内存（bcrypt 每次验证约 2^10 轮，是可控的慢操作）。
//
// 三者必须分开：请求预算成功不归还、也不清除失败历史；失败记账必须在锁内
// 原子预留，否则并发请求可以同时通过“尚未记账”的检查。
const (
	defaultAuthIPRequestRate     = 1.0
	defaultAuthIPRequestBurst    = 10
	defaultAuthGlobalRequestRate = 20.0
	defaultAuthGlobalBurst       = 40
	defaultAuthBcryptConcurrency = 4
	defaultCredentialBodyBytes   = 8 << 10
	defaultAuthGuardMaxKeys      = 4096
	defaultAuthCooldownMax       = 5 * time.Minute
	bcryptSlotWait               = 100 * time.Millisecond
	authGuardGCPeriod            = 5 * time.Minute
	authGuardKeyIdle             = 10 * time.Minute
	authGuardOverflowRate        = 1.0
	authGuardOverflowBurst       = 10

	authIPFailureLimit        = 20
	authIPFailureWindow       = 10 * time.Minute
	authAccountFailureLimit   = 10
	authAccountFailureWindow  = 15 * time.Minute
	authComboFailureLimit     = 5
	authComboFailureWindow    = 5 * time.Minute
	authRejectLogMinInterval  = 30 * time.Second
	authRejectLogMaxPerWindow = 1
)

// authGuardConfig 是经过归一化与校验后的认证预算配置。
type authGuardConfig struct {
	IPRequestRate             float64
	IPRequestBurst            int
	GlobalRequestRate         float64
	GlobalRequestBurst        int
	BcryptConcurrency         int
	CredentialBodyBytes       int64
	MaxKeys                   int
	AccountCooldownMaxSeconds int
}

func (c authGuardConfig) accountCooldownMax() time.Duration {
	if c.AccountCooldownMaxSeconds <= 0 {
		return defaultAuthCooldownMax
	}
	return time.Duration(c.AccountCooldownMaxSeconds) * time.Second
}

// authOutcome 是一次凭据尝试的终态。只有 InvalidPassword 会计入失败窗口；
// 存储故障不能算成密码错误，否则攻击者可以靠写库故障把合法用户锁死。
type authOutcome int

const (
	authOutcomeInterrupted authOutcome = iota
	authOutcomeInvalidPassword
	authOutcomePasswordVerified
	authOutcomeStorageFailure
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

type authFailureEntry struct {
	failures    int
	windowStart time.Time
	// blockedAt 仅在启用冷却上限（账号维度）时使用：达到阈值后最多冷却
	// AccountCooldownMax，避免账号被无限期锁死，也限制攻击者恶意锁号。
	blockedAt time.Time
}

// authTicket 一次凭据请求的预留凭据。Finish 幂等；所有失败路径都必须 Finish，
// 否则在途预留会永久占用失败额度。
type authTicket struct {
	guard      *authGuard
	ip         string
	accountKey string
	reserved   bool
	finished   bool
}

type authGuardCounters struct {
	RejectedIP        uint64 `json:"rejected_ip"`
	RejectedGlobal    uint64 `json:"rejected_global"`
	RejectedAccount   uint64 `json:"rejected_account"`
	RejectedCombo     uint64 `json:"rejected_combo"`
	RejectedSlot      uint64 `json:"rejected_slot"`
	RejectedOverflow  uint64 `json:"rejected_overflow"`
	InvalidPasswords  uint64 `json:"invalid_passwords"`
	Verified          uint64 `json:"verified"`
	StorageFailures   uint64 `json:"storage_failures"`
	RateLimitedLogged uint64 `json:"rate_limited_logged"`
}

// authGuard 是单进程认证预算器。它不声称跨重启/多实例共享；重启后清空，
// 部署侧边缘防护必须叠加，不能把这里当分布式配额。
type authGuard struct {
	mu       sync.Mutex
	now      func() time.Time
	cfg      authGuardConfig
	slots    chan struct{}
	active   atomic.Int64
	global   tokenBucket
	overflow tokenBucket
	ipTokens map[string]tokenBucket
	failures map[string]authFailureEntry
	inflight map[string]int
	lastGC   time.Time
	counters authGuardCounters
	// 拒绝日志聚合：按原因固定窗口最多打一条，避免攻击流量把磁盘写满。
	lastRejectLog map[string]time.Time
}

func newAuthGuard(cfg authGuardConfig) *authGuard {
	if cfg.BcryptConcurrency <= 0 {
		cfg.BcryptConcurrency = defaultAuthBcryptConcurrency
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = defaultAuthGuardMaxKeys
	}
	if cfg.IPRequestRate <= 0 {
		cfg.IPRequestRate = defaultAuthIPRequestRate
	}
	if cfg.IPRequestBurst <= 0 {
		cfg.IPRequestBurst = defaultAuthIPRequestBurst
	}
	if cfg.GlobalRequestRate <= 0 {
		cfg.GlobalRequestRate = defaultAuthGlobalRequestRate
	}
	if cfg.GlobalRequestBurst <= 0 {
		cfg.GlobalRequestBurst = defaultAuthGlobalBurst
	}
	if cfg.CredentialBodyBytes <= 0 {
		cfg.CredentialBodyBytes = defaultCredentialBodyBytes
	}
	clock := time.Now()
	return &authGuard{
		now:           time.Now,
		cfg:           cfg,
		slots:         make(chan struct{}, cfg.BcryptConcurrency),
		global:        tokenBucket{tokens: float64(cfg.GlobalRequestBurst), last: clock},
		overflow:      tokenBucket{tokens: authGuardOverflowBurst, last: clock},
		ipTokens:      make(map[string]tokenBucket),
		failures:      make(map[string]authFailureEntry),
		inflight:      make(map[string]int),
		lastGC:        clock,
		lastRejectLog: make(map[string]time.Time),
	}
}

func authIPKey(ip string) string { return "ip:" + ip }

// authAccountDigest 只保存用户名摘要：攻击者可以轮换超长/带控制字符的用户名，
// 不能让失败表保存攻击者可控的原文。认证语义仍使用原始（trim 后）用户名。
func authAccountDigest(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
	return "acct:" + hex.EncodeToString(sum[:16])
}

func authComboKey(ip, accountDigest string) string {
	if accountDigest == "" {
		return ""
	}
	return "combo:" + ip + "|" + accountDigest
}

// takeLocked 消耗一个令牌；返回 0 表示成功，否则是建议的重试等待时间。
func (g *authGuard) takeLocked(b *tokenBucket, rate, burst float64, now time.Time) time.Duration {
	if b.last.IsZero() {
		b.tokens = burst
		b.last = now
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(burst, b.tokens+elapsed*rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	wait := (1 - b.tokens) / rate
	if wait < 1 {
		wait = 1
	}
	return time.Duration(math.Ceil(wait*1000)) * time.Millisecond
}

// AdmitRequest 在解析请求体之前消耗全局与 IP 请求预算，并检查 IP 失败历史。
// 它不接受账号参数：账号只能在解析出用户名之后 AttachAccount，避免为了
// 提前拿到账号而读取攻击者控制的大 body。
func (g *authGuard) AdmitRequest(ip string) (*authTicket, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.gcLocked(now)

	if retry := g.takeLocked(&g.global, g.cfg.GlobalRequestRate, float64(g.cfg.GlobalRequestBurst), now); retry > 0 {
		g.counters.RejectedGlobal++
		g.rejectLogLocked("global", ip, retry, now)
		return nil, retry
	}
	// 失败表达到容量时，所有入场都要经过有界 overflow 桶。这样攻击者轮换
	// 无穷多个键时仍受全局速率约束，而不是每换一个键就白拿完整额度。
	if len(g.failures) >= g.cfg.MaxKeys {
		if retry := g.takeLocked(&g.overflow, authGuardOverflowRate, authGuardOverflowBurst, now); retry > 0 {
			g.counters.RejectedOverflow++
			g.rejectLogLocked("overflow", ip, retry, now)
			return nil, retry
		}
	}

	bucket, exists := g.ipTokens[ip]
	if !exists {
		if len(g.ipTokens) >= g.cfg.MaxKeys {
			if retry := g.takeLocked(&g.overflow, authGuardOverflowRate, authGuardOverflowBurst, now); retry > 0 {
				g.counters.RejectedOverflow++
				g.rejectLogLocked("overflow", ip, retry, now)
				return nil, retry
			}
		} else {
			bucket = tokenBucket{tokens: float64(g.cfg.IPRequestBurst), last: now}
			exists = true
		}
	}
	if exists {
		if retry := g.takeLocked(&bucket, g.cfg.IPRequestRate, float64(g.cfg.IPRequestBurst), now); retry > 0 {
			g.ipTokens[ip] = bucket
			g.counters.RejectedIP++
			g.rejectLogLocked("ip-request", ip, retry, now)
			return nil, retry
		}
		g.ipTokens[ip] = bucket
	}

	if retry := g.retryForKeyLocked(authIPKey(ip), authIPFailureLimit, authIPFailureWindow, 0, now); retry > 0 {
		g.counters.RejectedIP++
		g.rejectLogLocked("ip-failure", ip, retry, now)
		return nil, retry
	}
	return &authTicket{guard: g, ip: ip}, 0
}

// AttachAccount 在解析出用户名后原子预留一次失败额度。
// 预留期间 checks 会看到 failures+inflight，并发请求无法一起穿透阈值。
func (g *authGuard) AttachAccount(t *authTicket, username string) time.Duration {
	if t == nil {
		return 0
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return 0
	}
	accountKey := authAccountDigest(username)
	comboKey := authComboKey(t.ip, accountKey)

	g.mu.Lock()
	defer g.mu.Unlock()
	if t.finished || t.reserved {
		return 0
	}
	now := g.now()
	if retry := g.retryForKeyLocked(accountKey, authAccountFailureLimit, authAccountFailureWindow, g.cfg.accountCooldownMax(), now); retry > 0 {
		g.counters.RejectedAccount++
		g.rejectLogLocked("account", t.ip, retry, now)
		return retry
	}
	if retry := g.retryForKeyLocked(comboKey, authComboFailureLimit, authComboFailureWindow, 0, now); retry > 0 {
		g.counters.RejectedCombo++
		g.rejectLogLocked("combo", t.ip, retry, now)
		return retry
	}
	t.accountKey = accountKey
	t.reserved = true
	g.inflight[authIPKey(t.ip)]++
	g.inflight[accountKey]++
	g.inflight[comboKey]++
	return 0
}

// Finish 释放预留；只有密码错误才把预留转换成失败计数。幂等。
func (g *authGuard) Finish(t *authTicket, outcome authOutcome) {
	if t == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	now := g.now()
	if t.reserved {
		g.releaseLocked(authIPKey(t.ip))
		g.releaseLocked(t.accountKey)
		g.releaseLocked(authComboKey(t.ip, t.accountKey))
		t.reserved = false
	}
	switch outcome {
	case authOutcomeInvalidPassword:
		g.counters.InvalidPasswords++
		g.recordFailureLocked(authIPKey(t.ip), authIPFailureLimit, authIPFailureWindow, 0, now)
		if t.accountKey != "" {
			g.recordFailureLocked(t.accountKey, authAccountFailureLimit, authAccountFailureWindow, g.cfg.accountCooldownMax(), now)
			g.recordFailureLocked(authComboKey(t.ip, t.accountKey), authComboFailureLimit, authComboFailureWindow, 0, now)
		}
	case authOutcomePasswordVerified:
		g.counters.Verified++
	case authOutcomeStorageFailure:
		g.counters.StorageFailures++
	}
}

func (g *authGuard) releaseLocked(key string) {
	if key == "" {
		return
	}
	if n := g.inflight[key]; n > 1 {
		g.inflight[key] = n - 1
	} else {
		delete(g.inflight, key)
	}
}

func (g *authGuard) recordFailureLocked(key string, limit int, window, cooldown time.Duration, now time.Time) {
	entry, ok := g.failures[key]
	if ok && !now.Before(entry.windowStart.Add(window)) {
		delete(g.failures, key)
		ok = false
	}
	if !ok && len(g.failures) >= g.cfg.MaxKeys {
		// 表满不再创建新键；入场侧已经用 overflow 桶约束新键洪水。
		g.counters.RejectedOverflow++
		return
	}
	if !ok {
		entry = authFailureEntry{windowStart: now}
	}
	entry.failures++
	if cooldown > 0 && entry.failures >= limit && entry.blockedAt.IsZero() {
		entry.blockedAt = now
	}
	g.failures[key] = entry
}

func (g *authGuard) retryForKeyLocked(key string, limit int, window, cooldown time.Duration, now time.Time) time.Duration {
	if key == "" {
		return 0
	}
	entry, ok := g.failures[key]
	if ok && !now.Before(entry.windowStart.Add(window)) {
		delete(g.failures, key)
		ok = false
	}
	// 在途预留必须计入阈值，否则并发请求可以同时看到“尚未记账”而一起穿透。
	inflight := g.inflight[key]
	if ok {
		if entry.failures+inflight < limit {
			return 0
		}
	} else if inflight >= limit {
		// 只有在途预留时无法计算精确到期时间；给一个短等待，让预留落定。
		return time.Second
	} else {
		return 0
	}
	var until time.Time
	if cooldown > 0 && !entry.blockedAt.IsZero() {
		until = entry.blockedAt.Add(cooldown)
		if end := entry.windowStart.Add(window); until.After(end) {
			until = end
		}
		// 有界冷却到期后清零，避免账号被永久锁定；窗口本身继续约束后续猜密。
		if !now.Before(until) {
			delete(g.failures, key)
			return 0
		}
	} else {
		until = entry.windowStart.Add(window)
	}
	if !until.After(now) {
		return 0
	}
	return until.Sub(now)
}

// OnCredentialCommitSuccess 只在凭据签发/改密事务真正提交后调用。
// 它只清理对应账号与组合的失败历史，不清除 IP 喷洒历史，也不归还请求令牌。
func (g *authGuard) OnCredentialCommitSuccess(_ string, username string) {
	username = strings.TrimSpace(username)
	if username == "" {
		return
	}
	accountKey := authAccountDigest(username)
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, accountKey)
	for key := range g.failures {
		if strings.HasPrefix(key, "combo:") && strings.HasSuffix(key, "|"+accountKey) {
			delete(g.failures, key)
		}
	}
}

// Unlock 是运维应急入口：只清指定账号或来源的失败历史，不清请求桶与全局预算。
func (g *authGuard) Unlock(targetType, value string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(targetType)) {
	case "account":
		delete(g.failures, authAccountDigest(value))
		for key := range g.failures {
			if strings.HasPrefix(key, "combo:") && strings.HasSuffix(key, "|"+authAccountDigest(value)) {
				delete(g.failures, key)
			}
		}
		return true
	case "ip":
		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			return false
		}
		ip := addr.Unmap().String()
		delete(g.failures, authIPKey(ip))
		delete(g.ipTokens, ip)
		return true
	default:
		return false
	}
}

// AcquireBcrypt 获取一个 bcrypt 并发槽，最多等待 wait。等待必须有界，
// 不能为每个请求起 goroutine 无限排队。
func (g *authGuard) AcquireBcrypt(ctx context.Context, wait time.Duration) (func(), bool) {
	if wait <= 0 {
		select {
		case g.slots <- struct{}{}:
			g.active.Add(1)
			return g.releaseBcrypt, true
		default:
			g.mu.Lock()
			g.counters.RejectedSlot++
			g.mu.Unlock()
			return nil, false
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		g.active.Add(1)
		return g.releaseBcrypt, true
	case <-ctx.Done():
		g.mu.Lock()
		g.counters.RejectedSlot++
		g.mu.Unlock()
		return nil, false
	case <-timer.C:
		g.mu.Lock()
		g.counters.RejectedSlot++
		g.mu.Unlock()
		return nil, false
	}
}

func (g *authGuard) releaseBcrypt() {
	g.active.Add(-1)
	<-g.slots
}

func (g *authGuard) gcLocked(now time.Time) {
	if !g.lastGC.IsZero() && now.Sub(g.lastGC) < authGuardGCPeriod {
		return
	}
	g.lastGC = now
	for key, bucket := range g.ipTokens {
		if now.Sub(bucket.last) > authGuardKeyIdle && bucket.tokens >= float64(g.cfg.IPRequestBurst) {
			delete(g.ipTokens, key)
		}
	}
	for key, entry := range g.failures {
		window := authIPFailureWindow
		if strings.HasPrefix(key, "acct:") {
			window = authAccountFailureWindow
		} else if strings.HasPrefix(key, "combo:") {
			window = authComboFailureWindow
		}
		expired := !now.Before(entry.windowStart.Add(window))
		// 窗口到期即删除；账号的有界冷却由 retryForKeyLocked 在冷却结束时惰性删除。
		if expired {
			delete(g.failures, key)
		}
	}
	for reason, at := range g.lastRejectLog {
		if now.Sub(at) > 10*time.Minute {
			delete(g.lastRejectLog, reason)
		}
	}
}

func (g *authGuard) rejectLogLocked(reason, ip string, retry time.Duration, now time.Time) {
	if last, ok := g.lastRejectLog[reason]; ok && now.Sub(last) < authRejectLogMinInterval {
		return
	}
	g.lastRejectLog[reason] = now
	g.counters.RateLimitedLogged++
	seconds := 1
	if retry > time.Second {
		seconds = int(math.Ceil(retry.Seconds()))
	}
	slog.Warn("认证入口拒绝", "reason", reason, "source", safeAuthLogValue(ip), "retry_after_seconds", seconds)
}

// authGuardSnapshot 是管理页只读视图，不包含任何攻击者可控的用户名原文。
type authGuardSnapshot struct {
	Backend      string             `json:"backend"`
	Config       authGuardConfigDTO `json:"config"`
	Counters     authGuardCounters  `json:"counters"`
	FailureKeys  int                `json:"failure_keys"`
	InflightKeys int                `json:"inflight_keys"`
	IPTokenKeys  int                `json:"ip_token_keys"`
	Bcrypt       struct {
		Concurrency int   `json:"concurrency"`
		Active      int64 `json:"active"`
	} `json:"bcrypt"`
}

type authGuardConfigDTO struct {
	IPRequestRate             float64 `json:"ip_request_rate"`
	IPRequestBurst            int     `json:"ip_request_burst"`
	GlobalRequestRate         float64 `json:"global_request_rate"`
	GlobalRequestBurst        int     `json:"global_request_burst"`
	BcryptConcurrency         int     `json:"bcrypt_concurrency"`
	CredentialBodyBytes       int64   `json:"credential_body_bytes"`
	MaxKeys                   int     `json:"max_keys"`
	AccountCooldownMaxSeconds int     `json:"account_cooldown_max_seconds"`
	IPFailureLimit            int     `json:"ip_failure_limit"`
	IPFailureWindowSeconds    int     `json:"ip_failure_window_seconds"`
	AccountFailureLimit       int     `json:"account_failure_limit"`
	AccountFailureWindowSecs  int     `json:"account_failure_window_seconds"`
	ComboFailureLimit         int     `json:"combo_failure_limit"`
	ComboFailureWindowSeconds int     `json:"combo_failure_window_seconds"`
}

func (g *authGuard) Snapshot() authGuardSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := authGuardSnapshot{
		Backend:      "memory",
		Counters:     g.counters,
		FailureKeys:  len(g.failures),
		InflightKeys: len(g.inflight),
		IPTokenKeys:  len(g.ipTokens),
	}
	s.Config = authGuardConfigDTO{
		IPRequestRate:             g.cfg.IPRequestRate,
		IPRequestBurst:            g.cfg.IPRequestBurst,
		GlobalRequestRate:         g.cfg.GlobalRequestRate,
		GlobalRequestBurst:        g.cfg.GlobalRequestBurst,
		BcryptConcurrency:         g.cfg.BcryptConcurrency,
		CredentialBodyBytes:       g.cfg.CredentialBodyBytes,
		MaxKeys:                   g.cfg.MaxKeys,
		AccountCooldownMaxSeconds: int(g.cfg.accountCooldownMax().Seconds()),
		IPFailureLimit:            authIPFailureLimit,
		IPFailureWindowSeconds:    int(authIPFailureWindow.Seconds()),
		AccountFailureLimit:       authAccountFailureLimit,
		AccountFailureWindowSecs:  int(authAccountFailureWindow.Seconds()),
		ComboFailureLimit:         authComboFailureLimit,
		ComboFailureWindowSeconds: int(authComboFailureWindow.Seconds()),
	}
	s.Bcrypt.Concurrency = g.cfg.BcryptConcurrency
	s.Bcrypt.Active = g.active.Load()
	return s
}

// safeAuthLogValue 把可能来自请求的字符串收敛成单行、定长日志字段，
// 防止换行/控制字符伪造日志行。
func safeAuthLogValue(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
		if b.Len() >= 128 {
			break
		}
	}
	return b.String()
}

// authRateLimitedRetry 写 429 + Retry-After。retry 为正，避免 0 秒的重试建议。
func authRateLimitedRetry(c *gin.Context, retry time.Duration) {
	if retry <= 0 {
		retry = time.Second
	}
	seconds := int(math.Ceil(retry.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error": "尝试次数过多，请稍后再试", "code": "AUTH_RATE_LIMITED", "retryable": true,
		"retry_after": seconds,
	})
}

const (
	authLimitsPath = "/api/admin/security/auth-limits"
	authUnlockPath = "/api/admin/security/unlock"
)
