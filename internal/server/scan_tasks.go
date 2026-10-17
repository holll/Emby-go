package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/scanner"
)

// D10（已批）：长扫描改为进程内任务表 + 202/task_id + 1 小时幂等键。
// 明确边界：表在内存，重启即丢（不恢复、不补跑）；旧同步 POST /api/admin/scan
// 保留到验收集通过；任务表容量 200，满时只淘汰已完成任务，全在跑则拒绝。
const (
	scanTaskCapacity  = 200
	scanIdemTTL       = time.Hour
	maxIdempotencyKey = 128
)

var (
	errScanTaskCapacity = errors.New("任务表已满：没有可淘汰的已完成任务")
	errScanTaskNotFound = errors.New("task not found")
)

type asyncScanTask struct {
	ID         int64           `json:"task_id"`
	Type       string          `json:"type"`
	Status     string          `json:"status"` // queued/running/succeeded/failed/skipped
	LibraryID  int64           `json:"library_id"`
	CreatedAt  string          `json:"created_at"`
	StartedAt  string          `json:"started_at,omitempty"`
	FinishedAt string          `json:"finished_at,omitempty"`
	Result     *scanner.Result `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	IdemKey    string          `json:"-"`
}

type scanIdemEntry struct {
	taskID int64
	at     time.Time
}

func (t asyncScanTask) finished() bool {
	switch t.Status {
	case "succeeded", "failed", "skipped":
		return true
	}
	return false
}

// registerScanTask 登记一个已由 startTask 分配 ID 的任务。幂等键命中时直接
// 返回已有任务（created=false）。容量裁剪与幂等过期在同一把锁内完成。
func (a *App) registerScanTask(id int64, kind string, libraryID int64, idemKey string, now time.Time) (asyncScanTask, bool, error) {
	a.scanTaskMu.Lock()
	defer a.scanTaskMu.Unlock()
	a.pruneScanIdemLocked(now)
	if idemKey != "" {
		if entry, ok := a.scanTaskIdem[idemKey]; ok {
			if task, ok := a.scanTasks[entry.taskID]; ok {
				return *task, false, nil
			}
			delete(a.scanTaskIdem, idemKey)
		}
	}
	for len(a.scanTaskOrder) >= scanTaskCapacity {
		evicted := false
		for i, candidate := range a.scanTaskOrder {
			task, ok := a.scanTasks[candidate]
			if !ok || !task.finished() {
				continue
			}
			delete(a.scanTasks, candidate)
			a.scanTaskOrder = append(a.scanTaskOrder[:i], a.scanTaskOrder[i+1:]...)
			for key, entry := range a.scanTaskIdem {
				if entry.taskID == candidate {
					delete(a.scanTaskIdem, key)
				}
			}
			evicted = true
			break
		}
		if !evicted {
			return asyncScanTask{}, false, errScanTaskCapacity
		}
	}
	task := &asyncScanTask{
		ID:        id,
		Type:      kind,
		Status:    "queued",
		LibraryID: libraryID,
		CreatedAt: now.UTC().Format(time.RFC3339),
		IdemKey:   idemKey,
	}
	a.scanTasks[id] = task
	a.scanTaskOrder = append(a.scanTaskOrder, id)
	if idemKey != "" {
		a.scanTaskIdem[idemKey] = scanIdemEntry{taskID: id, at: now}
	}
	return *task, true, nil
}

func (a *App) pruneScanIdemLocked(now time.Time) {
	for key, entry := range a.scanTaskIdem {
		if now.Sub(entry.at) > scanIdemTTL {
			delete(a.scanTaskIdem, key)
		}
	}
}

// replayScanTask 返回 1 小时内同幂等键对应的任务。
func (a *App) replayScanTask(key string, now time.Time) (asyncScanTask, bool) {
	if key == "" {
		return asyncScanTask{}, false
	}
	a.scanTaskMu.Lock()
	defer a.scanTaskMu.Unlock()
	a.pruneScanIdemLocked(now)
	entry, ok := a.scanTaskIdem[key]
	if !ok {
		return asyncScanTask{}, false
	}
	task, ok := a.scanTasks[entry.taskID]
	if !ok {
		delete(a.scanTaskIdem, key)
		return asyncScanTask{}, false
	}
	return *task, true
}

func (a *App) scanTaskByID(id int64) (asyncScanTask, error) {
	a.scanTaskMu.Lock()
	defer a.scanTaskMu.Unlock()
	task, ok := a.scanTasks[id]
	if !ok {
		return asyncScanTask{}, errScanTaskNotFound
	}
	return *task, nil
}

func (a *App) setScanTaskRunning(id int64) {
	a.scanTaskMu.Lock()
	defer a.scanTaskMu.Unlock()
	if task, ok := a.scanTasks[id]; ok {
		task.Status = "running"
		task.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

func (a *App) finishScanTask(id int64, status string, result *scanner.Result, err error) {
	a.scanTaskMu.Lock()
	defer a.scanTaskMu.Unlock()
	task, ok := a.scanTasks[id]
	if !ok {
		return
	}
	task.Status = status
	task.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	task.Result = result
	if err != nil {
		task.Error = err.Error()
	}
}

// adminSubmitScan 是异步扫描入口（新路径 /api/admin/tasks/scan）。
// 成功 202 + task_id；扫描已在进行中 409；任务表满 429；幂等键 1 小时内重放。
func (a *App) adminSubmitScan(c *gin.Context) {
	now := time.Now()
	if key := strings.TrimSpace(c.GetHeader("Idempotency-Key")); key != "" {
		if len(key) > maxIdempotencyKey {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Idempotency-Key 过长", "code": "IDEMPOTENCY_KEY_TOO_LONG"})
			return
		}
		if task, ok := a.replayScanTask(key, now); ok {
			c.JSON(http.StatusAccepted, gin.H{
				"task_id": task.ID, "status": task.Status, "library_id": task.LibraryID,
				"created_at": task.CreatedAt, "replayed": true,
			})
			return
		}
		c.Set("idempotency-key", key)
	}
	if a.scanning() {
		c.JSON(http.StatusConflict, gin.H{"error": errScanBusy.Error(), "code": "SCAN_BUSY"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Query("library_id"), 10, 64)
	if err != nil && c.Query("library_id") != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "library_id 非法"})
		return
	}
	taskID := a.startTask("scan")
	task, created, err := a.registerScanTask(taskID, "scan", libraryID, c.GetString("idempotency-key"), now)
	if err != nil {
		a.finishTaskBusy(taskID, err.Error())
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error(), "code": "TASK_CAPACITY"})
		return
	}
	if !created {
		a.finishTaskBusy(taskID, "幂等重放")
		c.JSON(http.StatusAccepted, gin.H{"task_id": task.ID, "status": task.Status, "library_id": task.LibraryID, "replayed": true})
		return
	}
	a.workerWG.Add(1)
	go func() {
		defer a.workerWG.Done()
		a.runAsyncScan(task.ID, task.LibraryID)
	}()
	c.JSON(http.StatusAccepted, gin.H{"task_id": task.ID, "status": task.Status, "library_id": task.LibraryID})
}

func (a *App) runAsyncScan(id, libraryID int64) {
	a.setScanTaskRunning(id)
	result, err := a.scanLibraries(libraryID)
	switch {
	case err == nil:
		a.finishTask(id, nil)
		a.finishScanTask(id, "succeeded", &result, nil)
	case errors.Is(err, errScanBusy):
		a.finishTaskBusy(id, err.Error())
		a.finishScanTask(id, "skipped", nil, err)
	default:
		a.finishTask(id, err)
		a.finishScanTask(id, "failed", nil, err)
	}
}

// adminScanTaskStatus 返回异步任务状态；未知/被淘汰的 task_id 一律 404。
func (a *App) adminScanTaskStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errScanTaskNotFound.Error()})
		return
	}
	task, err := a.scanTaskByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "code": "TASK_NOT_FOUND"})
		return
	}
	c.JSON(http.StatusOK, task)
}
