package server

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"emby-go/internal/logging"
)

// Diagnostics contain route templates with validated uid/id parameters and an
// explicit scalar query allowlist. Other parameters (notably API keys), headers,
// bodies and arbitrary errors are never log payloads.
func requestLogger() gin.HandlerFunc { return requestLoggerTo(logging.RequestWriter()) }
func requestLoggerTo(w io.Writer) gin.HandlerFunc {
	logger := gin.LoggerWithConfig(gin.LoggerConfig{Output: w, Skip: skipRequestLog, Formatter: requestLogFormatter})
	return func(c *gin.Context) {
		c.Set("safe_request_route", diagnosticRoute(c))
		logger(c)
	}
}
func requestLogFormatter(p gin.LogFormatterParams) string {
	route, ok := p.Keys["safe_request_route"].(string)
	if !ok {
		route = unmatchedDiagnosticPath(p.Request.URL.Path)
	}
	path := route
	if query := redactedRequestQuery(p.Request.URL.RawQuery); query != "" {
		path += "?" + query
	}
	return fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s %q error=%t\n",
		p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency, p.ClientIP, diagnosticMethod(p.Method), path, p.ErrorMessage != "")
}
func diagnosticMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	}
	return "[OTHER]"
}
func diagnosticRoute(c *gin.Context) string {
	if path := c.FullPath(); path != "" {
		parts := strings.Split(path, "/")
		for i, part := range parts {
			switch part {
			case ":uid":
				if value := c.Param("uid"); diagnosticIDNumber(value) {
					parts[i] = value
				}
			case ":id":
				if value := c.Param("id"); diagnosticItemID(value) {
					parts[i] = value
				}
			}
		}
		return strings.Join(parts, "/")
	}
	return unmatchedDiagnosticPath(c.Request.URL.Path)
}

// Match the application's identifier shapes, not arbitrary path text. Limits
// bound log growth, and keeping unknown formats templated avoids logging pasted
// secrets or legacy free-form names. This never rewrites the actual request.
func diagnosticIDNumber(value string) bool {
	if len(value) == 0 || len(value) > 19 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 63)
	return err == nil
}

func diagnosticItemID(value string) bool {
	if diagnosticIDNumber(value) || value == "boxsets" {
		return true
	}
	if strings.HasPrefix(value, "part-") {
		parts := strings.Split(value, "-")
		return len(parts) == 3 && diagnosticIDNumber(parts[1]) && diagnosticIDNumber(parts[2])
	}
	prefix, encoded, ok := strings.Cut(value, ":")
	if !ok || encoded == "" || len(encoded) > 512 {
		return false
	}
	switch prefix {
	case "boxset", "genre", "tag", "studio", "person":
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		return err == nil && base64.RawURLEncoding.EncodeToString(decoded) == encoded
	}
	return false
}

func diagnosticItemIDs(value string) bool {
	if value == "" || len(value) > 2048 {
		return false
	}
	ids := strings.Split(value, ",")
	if len(ids) > 32 {
		return false
	}
	for _, id := range ids {
		if !diagnosticItemID(id) {
			return false
		}
	}
	return true
}

func diagnosticItemTypes(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, kind := range strings.Split(value, ",") {
		switch kind {
		case "Movie", "BoxSet", "CollectionFolder", "Folder", "Series", "Season", "Episode":
		default:
			return false
		}
	}
	return true
}

// Unknown path segments may themselves be credentials. Retain only a fixed
// resource family, not an attacker-controlled endpoint, ID or filename.
func unmatchedDiagnosticPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(path), "/"), "/")
	if len(parts) > 0 && parts[0] == "emby" {
		parts = parts[1:]
	}
	if len(parts) > 0 {
		switch parts[0] {
		case "users", "items", "videos", "sessions", "system", "persons", "audio":
			return "/" + parts[0] + "/[UNMATCHED]"
		}
	}
	return "/[UNMATCHED]"
}

// Only edits a diagnostic copy. The live query, path, headers and body are untouched.
func redactedRequestQuery(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) > 4096 {
		return "[REDACTED]"
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "[REDACTED]"
	}
	safe := url.Values{}
	for key, entries := range values {
		name := strings.ToLower(key)
		switch name {
		case "api_key", "apikey", "x-emby-token", "x-mediabrowser-token", "token", "access_token":
			safe.Set(name, "[REDACTED]")
		case "static", "recursive", "isplayed", "isfavorite":
			value := "[REDACTED]"
			if len(entries) == 1 && (entries[0] == "true" || entries[0] == "false") {
				value = entries[0]
			}
			safe.Set(key, value)
		case "userid", "itemid", "parentid", "mediasourceid":
			value := "[REDACTED]"
			if len(entries) == 1 && ((name == "userid" && diagnosticIDNumber(entries[0])) || (name != "userid" && diagnosticItemID(entries[0]))) {
				value = entries[0]
			}
			safe.Set(key, value)
		case "ids":
			value := "[REDACTED]"
			if len(entries) == 1 && diagnosticItemIDs(entries[0]) {
				value = entries[0]
			}
			safe.Set(key, value)
		case "includeitemtypes", "excludeitemtypes":
			value := "[REDACTED]"
			if len(entries) == 1 && diagnosticItemTypes(entries[0]) {
				value = entries[0]
			}
			safe.Set(key, value)
		case "limit", "startindex", "maxwidth", "maxheight", "width", "height", "quality":
			value := "[REDACTED]"
			if len(entries) == 1 && diagnosticNumber(entries[0]) {
				value = entries[0]
			}
			safe.Set(key, value)
		default:
			safe.Set("_redacted", "[REDACTED]")
		}
	}
	return safe.Encode()
}
func diagnosticNumber(value string) bool {
	if len(value) == 0 || len(value) > 12 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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

// Debug is metadata-only, including login, initialization, config and unknown
// routes. Never read even a preview of a body or enumerate arbitrary headers.
func requestContentLogger() gin.HandlerFunc { return requestContentLoggerTo(logging.RequestWriter()) }
func requestContentLoggerTo(w io.Writer) gin.HandlerFunc {
	return func(c *gin.Context) {
		fmt.Fprintf(w, "[API REQUEST] method=%s route=%q query=%q content_length=%d headers=[OMITTED] body=[OMITTED]\n", diagnosticMethod(c.Request.Method), diagnosticRoute(c), redactedRequestQuery(c.Request.URL.RawQuery), c.Request.ContentLength)
		c.Next()
	}
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
