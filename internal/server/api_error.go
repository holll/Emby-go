package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// apiError 统一 /api 错误信封：{"error": message, "code": code}。
// code 由 HTTP 状态推导，保证管理面错误都有稳定可分支的机器码；
// 需要更精确语义的端点仍可直接写 gin.H{"error":..., "code": "..."}（如 SCAN_BUSY）。
func apiError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message, "code": errorCodeForStatus(status)})
}

// errorCodeForStatus 是 v2 契约的一部分（API-CONTRACT.md §3）：按状态码给出通用 code。
func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_REQUEST"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusPreconditionRequired:
		return "PRECONDITION_REQUIRED"
	case http.StatusRequestEntityTooLarge:
		return "BODY_TOO_LARGE"
	case http.StatusUnsupportedMediaType:
		return "UNSUPPORTED_MEDIA_TYPE"
	case http.StatusTooManyRequests:
		return "TOO_MANY_REQUESTS"
	case http.StatusBadGateway:
		return "UPSTREAM_UNAVAILABLE"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}
