package server

import (
	"context"
	"errors"

	"emby-go/internal/metatube"
)

// Only bounded diagnostic categories and a numeric status may cross this log
// boundary. Do not stringify the error: URLs, credentials, proxy diagnostics
// and upstream-controlled status text can all be present in its message.
func backendHealthLogFields(err error) []any {
	if errors.Is(err, context.Canceled) {
		return []any{"reason", "canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return []any{"reason", "timeout"}
	}
	if apiErr, ok := errors.AsType[*metatube.APIError](err); ok {
		return []any{"reason", "http_status", "status", apiErr.StatusCode}
	}
	return []any{"reason", "request_failed"}
}
