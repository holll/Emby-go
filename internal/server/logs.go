package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"emby-go/internal/logging"
)

// adminLogs 返回进程内最近若干条运行日志（已脱敏），供 WebUI 运行日志面板展示。
// 只保留内存环形缓冲，不读取磁盘日志文件，避免路径穿越与任意文件读取。
func (a *App) adminLogs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	if limit <= 0 || limit > logging.RingSize {
		limit = 200
	}
	c.JSON(http.StatusOK, gin.H{"items": logging.Logs(limit)})
}
