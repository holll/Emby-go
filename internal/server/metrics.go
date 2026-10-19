package server

import (
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

// adminMetrics 输出进程与数据规模的最小指标集，供本机巡检/监控抓取。
// 只读、管理员鉴权；不引入 Prometheus 依赖，字段保持 JSON 稳定。
func (a *App) adminMetrics(c *gin.Context) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	var movies int
	for _, status := range []string{"success", "manual", "pending"} {
		count, err := a.db.CountByStatus(status)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "metrics unavailable")
			return
		}
		movies += count
	}
	users, err := a.db.CountUsers()
	if err != nil {
		apiError(c, http.StatusInternalServerError, "metrics unavailable")
		return
	}
	libraries, err := a.db.Libraries()
	if err != nil {
		apiError(c, http.StatusInternalServerError, "metrics unavailable")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"uptime_seconds": int64(time.Since(a.startAt).Seconds()),
		"goroutines":     runtime.NumGoroutine(),
		"heap_alloc":     mem.HeapAlloc,
		"heap_inuse":     mem.HeapInuse,
		"sys":            mem.Sys,
		"num_gc":         mem.NumGC,
		"movies":         movies,
		"users":          users,
		"libraries":      len(libraries),
		"cache_backend":  a.cacheBackend,
		"cache_online":   a.cacheOnline,
		"readiness":      a.ready.Load(),
	})
}
