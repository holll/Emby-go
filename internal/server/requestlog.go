package server

import (
	"fmt"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"emby-go/internal/logging"
)

// requestLogger 构造请求日志中间件：输出到请求日志文件（同时保留控制台），
// 并跳过静态资源/流/图片的 2xx 请求。
func requestLogger() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		Output:    logging.RequestWriter(),
		Skip:      skipRequestLog,
		Formatter: requestLogFormatter,
	})
}

func requestLogFormatter(p gin.LogFormatterParams) string {
	return fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s %#v\n%s",
		p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode,
		p.Latency, p.ClientIP, p.Method, redactedRequestPath(p.Path), p.ErrorMessage)
}

// 仅处理日志副本，不修改请求，避免影响鉴权及上游签名校验。
func redactedRequestPath(path string) string {
	base, rawQuery, found := strings.Cut(path, "?")
	if !found {
		return path
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		// 非法转义或分隔符可能藏有凭据，解析失败时整段隐藏。
		return base + "?[REDACTED]"
	}
	changed := false
	for key := range query {
		switch strings.ToLower(key) {
		case "api_key", "apikey", "x-emby-token", "x-mediabrowser-token", "token", "access_token":
			query.Set(key, "[REDACTED]")
			changed = true
		}
	}
	if !changed {
		return path
	}
	return base + "?" + query.Encode()
}

// skipRequestLog 判断该请求是否不记入请求日志。
// Gin 在 c.Next() 之后才调用本函数，此时状态码已确定：非 2xx 一律记录，
// 2xx 的静态资源/流/图片则跳过——否则海报墙与播放会把日志淹没。
// API 请求（含 /api/admin 与 Emby 兼容端点）始终记录。
func skipRequestLog(c *gin.Context) bool {
	status := c.Writer.Status()
	if status < 200 || status >= 300 {
		return false
	}
	return quietPath(c.Request.URL.Path)
}

// quietPath 判断路径是否属于高频噪声来源。Emby 路由同时注册了 PascalCase 与全小写
// 变体，客户端还会带 /emby 前缀，故先归一化（去前缀 + 转小写）再判断。
func quietPath(path string) bool {
	p := strings.ToLower(strings.TrimPrefix(strings.ToLower(path), "/emby"))
	switch {
	case p == "/favicon.ico", strings.HasPrefix(p, "/web/"):
		return true
	case strings.Contains(p, "/images/"):
		return true
	case strings.Contains(p, "/scrape/image"):
		// 刮削预览的缩略图代理：一次预览就是十几张图，与海报墙同类噪声。
		return true
	case strings.Contains(p, "/videos/") && (strings.Contains(p, "/stream") || strings.Contains(p, "/proxy")):
		return true
	case strings.HasPrefix(p, "/audio/"), p == "/audio":
		return true
	}
	return false
}

// requestContentLogger debug 模式下把完整请求（含 body 与头）转储到请求日志文件。
func requestContentLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		request, err := httputil.DumpRequest(c.Request, true)
		if err == nil {
			fmt.Fprintf(logging.RequestWriter(), "[API REQUEST]\n%s\n", string(request))
		}
		c.Next()
	}
}
