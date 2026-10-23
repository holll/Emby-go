package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
)

// rawSensitivePath 判断当前请求是否属于即使 debug 开启也不得原样落盘的
// 敏感路由（认证、改密/初始化、加解限流、API 密钥创建/删除/轮转）。
// 注意这里看的是真实路径；/emby 前缀只是 Emby 兼容别名。
func rawSensitivePath(path string) bool {
	p := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(path, "/emby"), "/"))
	switch p {
	case "/users/authenticatebyname", "/api/auth/initialize", "/api/auth/password",
		"/api/admin/security/unlock", "/api/admin/apikeys", "/api/admin/users":
		return true
	}
	return strings.HasPrefix(p, "/api/admin/apikeys/") || strings.HasPrefix(p, "/api/admin/users/")
}

// diagnosticURI 隐藏敏感路由整体与所有查询串。查询串里可能带 api_key/
// token（历史上也有把密钥放 URL 的兼容入口），不能进原始日志。
func diagnosticURI(c *gin.Context) string {
	if rawSensitivePath(c.Request.URL.Path) {
		return "[sensitive route]"
	}
	u := *c.Request.URL
	if u.RawQuery != "" || u.ForceQuery {
		u.RawQuery = "redacted"
		u.ForceQuery = false
	}
	return u.RequestURI()
}

func sensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-emby-token", "x-emby-authorization", "x-mediabrowser-token", "x-api-key":
		return true
	}
	return false
}

func safeHeaderDump(header http.Header) string {
	var buf bytes.Buffer
	for name, values := range header {
		for range values {
			if sensitiveHeader(name) {
				fmt.Fprintf(&buf, "%s: [redacted]\n", name)
				continue
			}
			// 逐值写，保持原有顺序与多值语义。
			fmt.Fprintf(&buf, "%s: %s\n", name, values[0])
			values = values[1:]
		}
	}
	return buf.String()
}

func requestDiagnostics(raw bool, requestLog, appLog io.Writer) []gin.HandlerFunc {
	if !raw {
		return []gin.HandlerFunc{requestLoggerTo(requestLog), requestBodyLimit(), requestRecoveryTo(appLog)}
	}
	return []gin.HandlerFunc{rawRequestLoggerTo(requestLog), rawRequestHeadersTo(requestLog),
		rawRequestRecoveryTo(requestLog), requestBodyLimit(), rawRequestBodyTo(requestLog)}
}

// Raw diagnostics are deliberately opt-in. Do not send them to slog, the
// in-memory administration log panel or the persistent unknown-route probe.
func rawRequestLoggerTo(w io.Writer) gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{Output: w, Formatter: func(p gin.LogFormatterParams) string {
		uri := p.Request.RequestURI
		if uri == "" && p.Request.URL != nil {
			uri = p.Request.URL.RequestURI()
		}
		if p.Request != nil {
			if rawSensitivePath(p.Request.URL.Path) {
				uri = "[sensitive route]"
			} else if p.Request.URL.RawQuery != "" {
				uri = p.Request.URL.Path + "?redacted"
			}
		}
		return fmt.Sprintf("[GIN DEBUG RAW] %s | %3d | %13v | %15s | %-7s %q error=%q\n",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency, p.ClientIP, p.Method, uri, p.ErrorMessage)
	}})
}

func rawRequestHeadersTo(w io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		fmt.Fprintf(w, "[API DEBUG RAW REQUEST] method=%q uri=%q host=%q headers=%q\n",
			c.Request.Method, diagnosticURI(c), c.Request.Host, safeHeaderDump(c.Request.Header))
		c.Next()
		if len(c.Errors) != 0 {
			fmt.Fprintf(w, "[API DEBUG RAW ERRORS] method=%q uri=%q errors=%q\n", c.Request.Method, diagnosticURI(c), c.Errors.String())
		}
	}
}

// Run after requestBodyLimit: debugging never disables the API's existing
// request-size budget. Preserve both the bytes and any read error for handlers.
// Quoting is lossless escaping, not redaction (including for binary uploads).
func rawRequestBodyTo(w io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rawSensitivePath(c.Request.URL.Path) {
			// 敏感路由完全不读取、不落盘请求体；debug 不是绕过安全边界的理由。
			fmt.Fprintf(w, "[API DEBUG RAW BODY] method=%q uri=%q body=[redacted sensitive route]\n", c.Request.Method, diagnosticURI(c))
			c.Next()
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			original := c.Request.Body
			body, err := io.ReadAll(original)
			var replay io.Reader = bytes.NewReader(body)
			if err != nil {
				replay = io.MultiReader(replay, debugBodyError{err})
			}
			c.Request.Body = &debugReplayBody{Reader: replay, Closer: original}
			fmt.Fprintf(w, "[API DEBUG RAW BODY] method=%q uri=%q body=%q read_error=%v\n", c.Request.Method, diagnosticURI(c), body, err)
		} else {
			fmt.Fprintf(w, "[API DEBUG RAW BODY] method=%q uri=%q body=\"\"\n", c.Request.Method, diagnosticURI(c))
		}
		c.Next()
	}
}

type debugReplayBody struct {
	io.Reader
	io.Closer
}

type debugBodyError struct{ err error }

func (r debugBodyError) Read([]byte) (int, error) { return 0, r.err }

func rawRequestRecoveryTo(w io.Writer) gin.HandlerFunc {
	// Keep Gin's broken-connection handling, without its separate header-masked
	// dump. Raw headers were already captured by the request middleware.
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		fmt.Fprintf(w, "[PANIC DEBUG RAW] value=%v\n%s\n", recovered, debug.Stack())
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
