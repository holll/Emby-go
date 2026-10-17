package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 请求体预算。JSON 端点都很小，只有图片上传需要更大空间。
// 不设上限时单个客户端就能把任意大小的请求体灌进内存或临时盘
// （JSON 在内存里解码，multipart 会落到临时文件）。
const (
	defaultRequestBodyLimit int64 = 1 << 20  // 1 MiB
	imageUploadBodyLimit    int64 = 32 << 20 // 32 MiB：海报/背景/横幅/媒体库封面
)

// bodyLimitFor 返回路径对应的请求体预算。白名单之外一律用默认预算，
// 不存在“不限量”的路径。
func bodyLimitFor(path string) int64 {
	if strings.HasPrefix(path, "/api/admin/items/") && strings.Contains(path, "/images/") {
		return imageUploadBodyLimit
	}
	if strings.HasPrefix(path, "/api/admin/libraries/") && strings.HasSuffix(path, "/cover") {
		return imageUploadBodyLimit
	}
	return defaultRequestBodyLimit
}

// requestBodyLimit 在 handler 读请求体之前施加预算。
// 声明了 Content-Length 且超预算的，直接 413。
// chunked 请求体由 MaxBytesReader 截断：handler 的 bind 会失败（通常 400），
// 连接随即关闭。这里只能做到尽力而为——handler 已经写出的响应无法再改成 413。
func requestBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := bodyLimitFor(c.Request.URL.Path)
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "request body too large", "code": "BODY_TOO_LARGE",
			})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// authAttemptLimit 是一个键在固定窗口内允许的失败次数。
type authAttemptLimit struct {
	max    int
	window time.Duration
}

// authRateLimiter 限制登录与初始化端点的失败尝试。
// 每个键一个固定窗口；登录成功会同时清掉 IP 与账号键。
// 它只是单进程内的刹车，不是分布式配额，也不能代替凭据强度或 TLS。
// 条目数有上限：攻击者轮换用户名时不会把 map 撑爆。
type authRateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	ip      authAttemptLimit
	account authAttemptLimit
	entries map[string]authAttemptEntry
	lastGC  time.Time
}

type authAttemptEntry struct {
	failures    int
	windowStart time.Time
}

const (
	authIPLimit          = 20
	authIPWindow         = 10 * time.Minute
	authAccountLimit     = 10
	authAccountWindow    = 15 * time.Minute
	maxAuthLimiterKeys   = 4096
	authLimiterGCPeriod  = 5 * time.Minute
	authLimiterMaxWindow = 15 * time.Minute
)

func newAuthRateLimiter() *authRateLimiter {
	return &authRateLimiter{
		now:     time.Now,
		ip:      authAttemptLimit{max: authIPLimit, window: authIPWindow},
		account: authAttemptLimit{max: authAccountLimit, window: authAccountWindow},
		entries: make(map[string]authAttemptEntry),
	}
}

func authIPKey(ip string) string { return "ip:" + ip }

func authAccountKey(username string) string {
	return "account:" + strings.ToLower(strings.TrimSpace(username))
}

// retryAfter 返回调用方还需等待多久；0 表示允许尝试。
func (l *authRateLimiter) retryAfter(ip, username string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.gcLocked(now)
	retry := l.retryForLocked(authIPKey(ip), l.ip, now)
	if username != "" {
		retry = max(retry, l.retryForLocked(authAccountKey(username), l.account, now))
	}
	return retry
}

func (l *authRateLimiter) retryForLocked(key string, limit authAttemptLimit, now time.Time) time.Duration {
	entry, ok := l.entries[key]
	if !ok || entry.failures < limit.max {
		return 0
	}
	until := entry.windowStart.Add(limit.window)
	if !now.Before(until) {
		return 0
	}
	return until.Sub(now)
}

// recordFailure 记一次失败。username 为空时只记 IP（初始化端点没有账号可记）。
func (l *authRateLimiter) recordFailure(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.gcLocked(now)
	l.recordLocked(authIPKey(ip), l.ip, now)
	if username != "" {
		l.recordLocked(authAccountKey(username), l.account, now)
	}
}

func (l *authRateLimiter) recordLocked(key string, limit authAttemptLimit, now time.Time) {
	entry, ok := l.entries[key]
	if !ok || !now.Before(entry.windowStart.Add(limit.window)) {
		if !ok && len(l.entries) >= maxAuthLimiterKeys {
			// 表满时放弃新键，而不是驱逐仍处于封禁期的条目。
			return
		}
		entry = authAttemptEntry{windowStart: now}
	}
	entry.failures++
	l.entries[key] = entry
}

// reset 在成功登录/初始化后清除两个键的失败计数。
func (l *authRateLimiter) reset(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, authIPKey(ip))
	if username != "" {
		delete(l.entries, authAccountKey(username))
	}
}

func (l *authRateLimiter) gcLocked(now time.Time) {
	if !l.lastGC.IsZero() && now.Sub(l.lastGC) < authLimiterGCPeriod {
		return
	}
	l.lastGC = now
	for key, entry := range l.entries {
		if !now.Before(entry.windowStart.Add(authLimiterMaxWindow)) {
			delete(l.entries, key)
		}
	}
}
