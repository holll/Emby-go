package server

import (
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

// adminDiagnostics 聚合只读证据：版本、库结构、数据量、任务/调度、探测与缓存状态。
// 默认不发起任何网络请求；ffprobe 只报告配置的路径与探测结果，不现场执行。
func (a *App) adminDiagnostics(c *gin.Context) {
	now := time.Now().UTC()
	dbInfo, err := os.Stat(a.cfg.DBPath)
	if err != nil {
		apiError(c, 500, "读取数据库文件信息失败："+err.Error())
		return
	}
	schemaVersion, err := a.db.SchemaVersion()
	if err != nil {
		apiError(c, 500, "读取数据库结构版本失败："+err.Error())
		return
	}
	taskRecords, err := a.db.TaskRecordCount()
	if err != nil {
		apiError(c, 500, "读取任务摘要数量失败："+err.Error())
		return
	}
	movies := gin.H{}
	for _, status := range []string{"success", "pending", "manual"} {
		count, err := a.db.CountByStatus(status)
		if err != nil {
			apiError(c, 500, err.Error())
			return
		}
		movies[status] = count
	}
	libraries, err := a.db.Libraries()
	if err != nil {
		apiError(c, 500, err.Error())
		return
	}
	scheduled, err := a.db.ScheduledTasks()
	if err != nil {
		apiError(c, 500, err.Error())
		return
	}
	schedules := make([]gin.H, 0, len(scheduled))
	enabledSchedules := 0
	for _, task := range scheduled {
		item := gin.H{
			"id": task.ID, "name": task.Name, "type": task.Type, "cron": task.Cron,
			"enabled": task.Enabled, "last_run_at": task.LastRunAt,
			"last_status": task.LastStatus, "last_message": task.LastMessage,
		}
		if task.Enabled {
			enabledSchedules++
			if next := a.sched.Next(task.ID); !next.IsZero() {
				item["next_run"] = next.UTC().Format(time.RFC3339)
			}
		}
		schedules = append(schedules, item)
	}

	a.probeTaskMu.RLock()
	probe := gin.H{
		"running": a.probeStatus.Running, "total": a.probeStatus.Total, "done": a.probeStatus.Done,
		"failed": a.probeStatus.Failed, "started_at": a.probeStatus.StartedAt,
		"finished_at": a.probeStatus.FinishedAt, "error": a.probeStatus.Error,
	}
	a.probeTaskMu.RUnlock()

	a.scrapeTaskMu.RLock()
	scrape := gin.H{
		"running": a.scrapeStatus.Running, "kind": a.scrapeStatus.Kind,
		"total": a.scrapeStatus.Total, "done": a.scrapeStatus.Done,
		"failed": a.scrapeStatus.Failed, "started_at": a.scrapeStatus.StartedAt,
	}
	a.scrapeTaskMu.RUnlock()

	a.webhookMu.RLock()
	webhookTotal, webhookEnabled := len(a.webhookTargets), 0
	for _, target := range a.webhookTargets {
		if target.Enabled {
			webhookEnabled++
		}
	}
	webhookMulti := a.webhookMulti
	a.webhookMu.RUnlock()

	imageCache := gin.H{"enabled": false}
	if stats, err := a.imgDisk.Stats(); err == nil {
		imageCache = gin.H{
			"enabled": a.imgDisk.Enabled(), "dir": a.imgDisk.Dir(),
			"files": stats.Files, "bytes": stats.Bytes, "max_bytes": a.imgDisk.MaxBytes(),
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"generated_at": now.Format(time.RFC3339),
		"app": gin.H{
			"version": BuildVersion, "commit": BuildCommit, "build_time": BuildTime,
			"started_at":     a.startAt.UTC().Format(time.RFC3339),
			"uptime_seconds": int64(now.Sub(a.startAt).Seconds()),
			"go_version":     runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		},
		"store": gin.H{
			"path": a.cfg.DBPath, "size_bytes": dbInfo.Size(), "schema_version": schemaVersion,
			"movies": movies, "libraries": len(libraries),
			"task_records":    taskRecords,
			"scheduled_tasks": gin.H{"total": len(scheduled), "enabled": enabledSchedules},
		},
		"runtime": gin.H{
			"scan_running": a.scanning(), "probe": probe, "scrape": scrape,
		},
		"scheduler":   schedules,
		"webhook":     gin.H{"targets": webhookTotal, "enabled_targets": webhookEnabled, "multi_target": webhookMulti},
		"image_cache": imageCache,
		"ffprobe":     gin.H{"path": a.ffprobePath, "error": a.ffprobeError},
	})
}
