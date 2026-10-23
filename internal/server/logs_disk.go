package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"emby-go/internal/logging"
)

// adminDiskLogs 是 R4 的受限磁盘日志读取接口。
// 只接受固定 kind=app；不接收任何路径/文件名，日期文件与行 offset 全在
// internal/logging 内用受限根 + HMAC 游标校验，伪造游标返回 400 而非被信任。
func (a *App) adminDiskLogs(c *gin.Context) {
	kind := c.DefaultQuery("kind", logging.LogKindApp)
	if kind != logging.LogKindApp {
		apiError(c, http.StatusBadRequest, "暂只支持 kind=app；请求日志可能含原始调试内容，不在此接口暴露")
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			apiError(c, http.StatusBadRequest, "limit 必须是 1–500 的整数")
			return
		}
		limit = value
	}
	after, before := c.Query("after"), c.Query("before")
	if after != "" && before != "" {
		apiError(c, http.StatusBadRequest, "after 与 before 不能同时使用")
		return
	}
	page, err := logging.ReadDiskLogs(logging.DiskLogQuery{After: after, Before: before, Limit: limit})
	switch {
	case errors.Is(err, logging.ErrLogsCursorInvalid):
		apiError(c, http.StatusBadRequest, "日志游标无效或已过期，请重新加载")
		return
	case errors.Is(err, logging.ErrLogsUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "磁盘日志暂时不可读，请检查服务器日志目录权限后重试",
			"code":  "LOGS_UNAVAILABLE",
		})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取日志失败", "code": "LOGS_INTERNAL"})
		return
	}
	items := page.Items
	if items == nil {
		items = []logging.DiskLogItem{}
	}
	c.JSON(http.StatusOK, gin.H{
		"source":      "disk",
		"kind":        kind,
		"items":       items,
		"next_after":  page.NextAfter,
		"next_before": page.NextBefore,
		"has_more":    page.HasMore,
		"reset":       page.Reset,
		"gap":         page.Gap,
	})
}
