package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/backup"
)

// N-05：固定范围备份。快照含凭据材料（密码哈希/令牌/API Key）与媒体库 NFO，
// 因此只写到数据库同级的 backups/（本进程先以 0700 建目录），不提供网页下载、
// 恢复、任意路径或匿名访问；结果只回显元数据。
const backupRetention = 7

func (a *App) backupDir() string {
	return filepath.Join(filepath.Dir(a.cfg.DBPath), "backups")
}

type backupRunView struct {
	Dir                string   `json:"dir"`
	NFOCount           int      `json:"nfo_count"`
	ExtraBackdropCount int      `json:"extra_backdrop_count"`
	Pruned             []string `json:"pruned"`
	FinishedAt         string   `json:"finished_at"`
}

func newBackupRunView(result backup.Result) backupRunView {
	return backupRunView{
		Dir: result.Dir, NFOCount: result.NFOCount, ExtraBackdropCount: result.ExtraBackdropCount,
		Pruned: result.Pruned, FinishedAt: result.FinishedAt.UTC().Format(time.RFC3339),
	}
}

// adminBackupStatus 返回固定备份目录、留存策略与最近一次结果。只读，不访问媒体盘。
func (a *App) adminBackupStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	dir := a.backupDir()
	last := gin.H(nil)
	rec, found, err := a.db.LatestTaskRecordOfType("backup")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取备份记录失败"})
		return
	}
	if found {
		item := gin.H{
			"task_id": rec.ID, "status": rec.Status, "started_at": rec.StartedAt,
			"finished_at": rec.FinishedAt, "error": rec.Error,
		}
		if rec.ResultJSON != "" {
			var view backupRunView
			if json.Unmarshal([]byte(rec.ResultJSON), &view) == nil {
				item["result"] = view
			}
		}
		last = item
	}
	c.JSON(http.StatusOK, gin.H{
		"dir": dir, "retention": backupRetention, "running": a.backupRunning.Load(), "last": last,
	})
}

// adminBackupRun 在后台执行一次固定范围备份；同一时刻只允许一路。
func (a *App) adminBackupRun(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !a.admitAdminOperation(c) {
		return
	}
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	if !a.backupRunning.CompareAndSwap(false, true) {
		release()
		apiError(c, http.StatusConflict, "已有备份正在进行，请等待完成")
		return
	}
	dir := a.backupDir()
	// 备份含凭据材料：目录先以 0700 建立，backup.Run 只会在已存在目录内写文件。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.backupRunning.Store(false)
		release()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建备份目录失败：" + err.Error()})
		return
	}
	taskID := a.startTask("backup")
	c.JSON(http.StatusAccepted, gin.H{"task_id": taskID})
	go func() {
		defer release()
		defer a.backupRunning.Store(false)
		result, err := backup.Run(backup.Options{
			DBPath: a.cfg.DBPath, OutDir: dir, Retain: backupRetention, NFORoot: true, ExtraBackdrops: true,
		})
		if err != nil {
			a.finishTask(taskID, err)
			return
		}
		a.finishTask(taskID, nil)
		payload, marshalErr := json.Marshal(newBackupRunView(result))
		if marshalErr != nil {
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if e := a.db.FinishTaskRecord(taskID, "success", now, "", string(payload)); e != nil {
			slog.Warn("备份结果摘要写入失败", "task_id", taskID, "error", e)
		}
	}()
}
