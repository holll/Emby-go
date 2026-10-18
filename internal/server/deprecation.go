package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// deprecatedEndpoint 是 v2 弃用登记表的一条记录。
// v2 只登记与提示，不删除实现；移除需满足 Sunset 且升 v3（见 API-CONTRACT.md §5）。
type deprecatedEndpoint struct {
	Method    string
	Path      string
	Successor string
	Since     string
	Sunset    time.Time
}

var legacySyncScan = deprecatedEndpoint{
	Method:    http.MethodPost,
	Path:      "/api/admin/scan",
	Successor: "/api/admin/tasks/scan",
	Since:     "v2.0.0",
	Sunset:    time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC),
}

// deprecatedEndpoints 是全部登记项；契约测试会核对每项都有替代路径与 Sunset。
var deprecatedEndpoints = []deprecatedEndpoint{legacySyncScan}

// markDeprecated 按 RFC 8594（Deprecation/Sunset）与 RFC 8288（Link）写弃用头。
// 头只表示“将被移除”，不改变状态码与响应体，旧客户端可继续使用。
func markDeprecated(c *gin.Context, d deprecatedEndpoint) {
	c.Header("Deprecation", "true")
	c.Header("Sunset", d.Sunset.Format(http.TimeFormat))
	c.Header("Link", "<"+d.Successor+`>; rel="successor-version"`)
	c.Header("Warning", `299 - "deprecated endpoint; use `+d.Method+" "+d.Successor+`"`)
}
