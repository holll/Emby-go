package logging

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

// 统一安全日志层：在写磁盘与 ring 之前脱敏。这里不是“凭据检测器”，
// 而是有明确边界的白名单/黑名单组合：
//   - 结构化属性：敏感 key 的值整体替换，嵌套分组递归处理；
//   - 自由文本：key=value、Authorization/Bearer、URL 查询串、URL userinfo。
//
// 无法解析的未知格式不会被宣称安全；读取侧仍会再清洗一次历史记录。
var sensitiveLogKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true,
	"token": true, "access_token": true, "refresh_token": true,
	"secret": true, "api_key": true, "apikey": true, "x-api-key": true,
	"authorization": true, "proxy-authorization": true,
	"cookie": true, "set-cookie": true,
	"x-emby-token": true, "x-emby-authorization": true, "x-mediabrowser-token": true,
	"webhook_url": true, "webhook": true, "url": true, "target_url": true,
	"signed_url": true, "stream_url": true, "ffprobe_stderr": true, "stderr": true,
}

func isSensitiveLogKey(key string) bool {
	return sensitiveLogKeys[strings.ToLower(strings.TrimSpace(key))]
}

var (
	logKeyValuePattern = regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|api[_-]?key|apikey|authorization|cookie|set-cookie|x-emby-token|x-emby-authorization|x-mediabrowser-token)(\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\s,;&]+)`)
	logBearerPattern   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{4,}`)
	logURLQueryPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s"'<>]*?)\?[^\s"'<>]*`)
	logUserInfoPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/@\s"'<>]+@`)
)

// SanitizeText 清洗一段自由文本；输出保持原长度量级，避免日志被放大。
func SanitizeText(s string) string {
	if s == "" {
		return s
	}
	// Bearer/Basic 先处理：否则 "authorization: Bearer xxx" 会把 Bearer
	// 当值替换，令牌本体反而残留。
	s = logBearerPattern.ReplaceAllString(s, "$1 [REDACTED]")
	s = logKeyValuePattern.ReplaceAllString(s, "$1$2[REDACTED]")
	s = logURLQueryPattern.ReplaceAllString(s, "$1?[REDACTED]")
	s = logUserInfoPattern.ReplaceAllString(s, "$1[REDACTED]@")
	return s
}

// sanitizingHandler 在 TextHandler/JSONHandler 序列化之前处理属性。
type sanitizingHandler struct{ slog.Handler }

func newSanitizingHandler(h slog.Handler) slog.Handler { return sanitizingHandler{h} }

func (h sanitizingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, SanitizeText(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(sanitizeLogAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, out)
}

func (h sanitizingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = sanitizeLogAttr(a)
	}
	return sanitizingHandler{h.Handler.WithAttrs(out)}
}

func (h sanitizingHandler) WithGroup(name string) slog.Handler {
	return sanitizingHandler{h.Handler.WithGroup(name)}
}

func sanitizeLogAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if isSensitiveLogKey(a.Key) {
		a.Value = slog.StringValue("[REDACTED]")
		return a
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		attrs := a.Value.Group()
		clean := make([]slog.Attr, len(attrs))
		for i, child := range attrs {
			clean[i] = sanitizeLogAttr(child)
		}
		a.Value = slog.GroupValue(clean...)
	case slog.KindString:
		a.Value = slog.StringValue(SanitizeText(a.Value.String()))
	case slog.KindAny:
		if text, ok := a.Value.Any().(string); ok {
			a.Value = slog.StringValue(SanitizeText(text))
		} else if err, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(SanitizeText(err.Error()))
		}
	}
	return a
}
