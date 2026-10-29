package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"emby-go/internal/scheduler"
	"emby-go/internal/store"
)

type scheduledSpriteParams struct {
	LibraryID int64 `json:"library_id"`
	Limit     int   `json:"limit"`
	Overwrite bool  `json:"overwrite"`
}

// Unlike legacy coercive parsers, explicit null, fractions, strings, unknown
// fields and negative scopes are rejected; zero means all eligible targets.
func parseScheduledSpriteParams(raw map[string]any) (scheduledSpriteParams, error) {
	var p scheduledSpriteParams
	for key, value := range raw {
		if key != "library_id" && key != "limit" && key != "overwrite" {
			return p, fmt.Errorf("unknown sprites parameter %q", key)
		}
		if value == nil {
			return p, fmt.Errorf("sprites %s must not be null", key)
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return p, errors.New("invalid sprites parameters")
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, errors.New("sprites parameters require integer library_id/limit and boolean overwrite")
	}
	if p.LibraryID < 0 || p.Limit < 0 || p.Limit > 1000000 {
		return p, errors.New("sprites library_id must be nonnegative; limit must be 0 to 1000000")
	}
	return p, nil
}

// failScheduledSprites 处理「还没进入任务列表」就失败的雪碧图执行：
// 这类失败原先只返回一句固定文案，任务页没有条目、底层原因也无处可查。
// 这里补一条任务记录（管理端任务页可见），把底层原因用 %w 留给 scheduler
// 写进计划任务「上次结果」，并单独记一条带任务名的错误日志。
func (a *App) failScheduledSprites(task scheduler.Task, stage string, cause error) error {
	err := fmt.Errorf("雪碧图计划任务失败（%s）: %w", stage, cause)
	id := a.startTask("video_sprites")
	slog.Error("雪碧图计划任务失败", "name", task.Name, "type", task.Type, "task_id", id, "stage", stage, "error", cause)
	a.finishTask(id, err)
	return err
}

func (a *App) runScheduledSprites(parent context.Context, task scheduler.Task) error {
	p, err := parseScheduledSpriteParams(task.Params)
	if err != nil {
		return a.failScheduledSprites(task, "参数无效", err)
	}
	if err = parent.Err(); err != nil {
		slog.Debug("雪碧图计划任务未执行：调度已取消", "name", task.Name, "task_id", task.ID, "error", err)
		return err
	}
	if err = a.rootCtx.Err(); err != nil {
		slog.Debug("雪碧图计划任务未执行：服务正在关停", "name", task.Name, "task_id", task.ID, "error", err)
		return err
	}
	opts, err := a.spriteOptions()
	if err != nil {
		return a.failScheduledSprites(task, "ffmpeg 或 ffprobe 不可用", err)
	}
	total, maxID, err := a.db.ScheduledSpriteScope(parent, p.LibraryID)
	if err != nil {
		return a.failScheduledSprites(task, "视频预览媒体库范围不可用", err)
	}
	if p.Limit > 0 && total > p.Limit {
		total = p.Limit
	}
	a.sprites.mu.Lock()
	if a.rootCtx.Err() != nil || parent.Err() != nil {
		a.sprites.mu.Unlock()
		slog.Debug("雪碧图计划任务未执行：服务正在关停或调度已取消", "name", task.Name, "task_id", task.ID)
		return context.Canceled
	}
	// 跳过（scheduler.ErrBusy）也要能定位：区分「上一轮仍在跑」与「别的
	// 媒体维护任务占着 NFO 锁」，两种情况运维处理方式完全不同。
	if a.sprites.status.Running {
		a.sprites.mu.Unlock()
		slog.Info("雪碧图计划任务跳过：上一轮生成仍在执行", "name", task.Name, "task_id", task.ID)
		return fmt.Errorf("%w：上一轮雪碧图生成仍在执行", scheduler.ErrBusy)
	}
	if !a.claimNFO("sprites") {
		a.sprites.mu.Unlock()
		slog.Info("雪碧图计划任务跳过：刮削/翻译等媒体维护任务正在占用", "name", task.Name, "task_id", task.ID)
		return fmt.Errorf("%w：刮削或翻译等媒体维护任务正在占用", scheduler.ErrBusy)
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	stopParent := context.AfterFunc(parent, cancel)
	defer stopParent()
	id := a.startTask("video_sprites")
	a.sprites.status = spriteStatus{InstanceID: a.instanceID, TaskID: id, MovieIDs: []int64{}, Running: true, Total: total}
	a.sprites.cancel = cancel
	a.sprites.mu.Unlock()
	// 开始日志带上解析后的作用域与目标数：任务参数是 JSON，
	// 用户在日志里要能直接看到「这次到底要生成多少、覆盖哪个库」。
	slog.Info("雪碧图计划任务开始", "name", task.Name, "task_id", id,
		"library_id", p.LibraryID, "limit", p.Limit, "overwrite", p.Overwrite, "total", total)
	// A scheduler run is synchronous: cron.Stop waits for it, and manual RunNow
	// is already registered with workerWG by adminRunScheduledTask.
	after := int64(0)
	remaining := total
	var page []store.ScheduledSpriteTarget
	intervals := make(map[int64]int)
	req := spriteRequest{Overwrite: p.Overwrite, next: func(ctx context.Context) (int64, int, bool, error) {
		if remaining == 0 {
			return 0, 0, false, nil
		}
		if len(page) == 0 {
			size := min(remaining, 200)
			var e error
			page, e = a.db.ScheduledSpriteTargets(ctx, p.LibraryID, after, maxID, size)
			if e != nil {
				return 0, 0, false, e
			}
			if len(page) == 0 {
				return 0, 0, false, errors.New("sprite target scope changed during execution")
			}
		}
		target := page[0]
		page = page[1:]
		after = target.ID
		remaining--
		interval, ok := intervals[target.LibraryID]
		if !ok {
			var e error
			interval, e = a.db.LibrarySpriteInterval(ctx, target.LibraryID)
			if e != nil {
				return 0, 0, false, e
			}
			intervals[target.LibraryID] = interval
		}
		return target.ID, interval, true, nil
	}}
	return a.executeSprites(ctx, id, req, opts)
}
