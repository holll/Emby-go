package server

import "github.com/gin-gonic/gin"

// cspReportOnly 先以 Report-Only 观察（D04/复审 P2-8）：确认内联样式、播放器
// blob 与远程图片/流均被覆盖后再转强制；违规只进浏览器控制台，不影响功能。
const cspReportOnly = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https: http:; media-src 'self' blob: https: http:; " +
	"connect-src 'self'; font-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; " +
	"form-action 'self'; object-src 'none'"

// securityHeaders 对所有响应生效，包括 404/401 与图片/流。
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy-Report-Only", cspReportOnly)
		c.Next()
	}
}
