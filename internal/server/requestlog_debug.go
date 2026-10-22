package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

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
		if uri == "" {
			uri = p.Request.URL.RequestURI()
		}
		return fmt.Sprintf("[GIN DEBUG RAW] %s | %3d | %13v | %15s | %-7s %q error=%q\n",
			p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency, p.ClientIP, p.Method, uri, p.ErrorMessage)
	}})
}

func rawRequestHeadersTo(w io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var headers bytes.Buffer
		_ = c.Request.Header.Write(&headers)
		fmt.Fprintf(w, "[API DEBUG RAW REQUEST] method=%q uri=%q host=%q headers=%q\n", c.Request.Method, c.Request.RequestURI, c.Request.Host, headers.String())
		c.Next()
		if len(c.Errors) != 0 {
			fmt.Fprintf(w, "[API DEBUG RAW ERRORS] method=%q uri=%q errors=%q\n", c.Request.Method, c.Request.RequestURI, c.Errors.String())
		}
	}
}

// Run after requestBodyLimit: debugging never disables the API's existing
// request-size budget. Preserve both the bytes and any read error for handlers.
// Quoting is lossless escaping, not redaction (including for binary uploads).
func rawRequestBodyTo(w io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			original := c.Request.Body
			body, err := io.ReadAll(original)
			var replay io.Reader = bytes.NewReader(body)
			if err != nil {
				replay = io.MultiReader(replay, debugBodyError{err})
			}
			c.Request.Body = &debugReplayBody{Reader: replay, Closer: original}
			fmt.Fprintf(w, "[API DEBUG RAW BODY] method=%q uri=%q body=%q read_error=%v\n", c.Request.Method, c.Request.RequestURI, body, err)
		} else {
			fmt.Fprintf(w, "[API DEBUG RAW BODY] method=%q uri=%q body=\"\"\n", c.Request.Method, c.Request.RequestURI)
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
