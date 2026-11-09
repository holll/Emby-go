package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Request URLs are deliberately logged verbatim, including query credentials.
// Headers, bodies and arbitrary errors are not added to the ordinary log.
func requestLoggerTo(w io.Writer) gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{Output: w, Skip: skipRequestLog, Formatter: requestLogFormatter})
}

func requestLogFormatter(p gin.LogFormatterParams) string {
	return fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s %q error=%t\n",
		p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency, p.ClientIP, diagnosticMethod(p.Method), requestURI(p.Request), p.ErrorMessage != "")
}

// Prefer the original request target so escaped paths, query order and an empty
// trailing query remain intact. Constructed requests may only have a URL.
func requestURI(r *http.Request) string {
	if r.RequestURI != "" {
		return r.RequestURI
	}
	return r.URL.RequestURI()
}

func diagnosticMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	}
	return "[OTHER]"
}

// Skip only successful high-frequency resources; errors and APIs remain observable.
func skipRequestLog(c *gin.Context) bool {
	status := c.Writer.Status()
	if status < 200 || status >= 300 {
		return false
	}
	return quietPath(c.Request.URL.Path)
}
func quietPath(path string) bool {
	p := strings.ToLower(strings.TrimPrefix(strings.ToLower(path), "/emby"))
	switch {
	case p == "/favicon.ico", strings.HasPrefix(p, "/web/"):
		return true
	case strings.Contains(p, "/images/"), strings.Contains(p, "/scrape/image"):
		return true
	case strings.Contains(p, "/videos/") && (strings.Contains(p, "/stream") || strings.Contains(p, "/proxy")):
		return true
	case strings.HasPrefix(p, "/audio/"), p == "/audio":
		return true
	}
	return false
}

// Gin's default recovery dumps the request and panic value. Suppress that dump;
// retain recovery/broken-connection behavior, but emit no untrusted error text.
func requestRecoveryTo(w io.Writer) gin.HandlerFunc {
	// A nil writer also prevents Gin from constructing a raw dump/stack to discard.
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, _ any) {
		fmt.Fprintln(w, "[PANIC] request aborted; request and panic details omitted")
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
