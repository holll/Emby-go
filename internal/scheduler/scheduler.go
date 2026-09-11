// Package scheduler 按 cron 表达式调度计划任务。
//
// 职责边界：只负责「解析表达式、到点触发、判定执行结果、记录上次结果」，
// 具体做什么由调用方注入的 Runner 决定（本包不认识扫库/探测/刮削）。
//
// 规则（见需求 A3）：
//   - 标准 5 段表达式 + @daily/@hourly/@weekly/@monthly 等描述符，服务器本地时区
//   - 停机期间错过的执行不补跑
//   - 同一任务上一轮未结束时跳过本次并记录（由 Runner 返回 ErrBusy 表达）
//   - 不持久化执行历史，只在任务行上保留「上次执行时间/结果/原因」
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"emby-go/internal/store"
)

// ErrBusy 由 Runner 返回，表示同类任务有上一轮仍在执行——本次跳过而非失败。
var ErrBusy = errors.New("任务正在进行中")

// 执行结果状态，与 store.ScheduledTask.LastStatus 一致。
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Task 一次计划任务的运行期视图（Params 已从 JSON 解码）。
type Task struct {
	ID     int64
	Name   string
	Type   string
	Cron   string
	Params map[string]any
}

// Runner 执行一次任务；返回 ErrBusy 表示跳过，其它错误记为失败。
type Runner func(ctx context.Context, task Task) error

// Scheduler 管理全部计划任务。零值不可用，请用 New 构造。
type Scheduler struct {
	store  *store.Store
	runner Runner
	ctx    context.Context

	cron *cron.Cron

	// entries 记录 cron 内部 entry id（供 Stop 清理），schedules 保留解析后的
	// 调度计划用于自算「下次执行时间」——cron.Entry.Next 只在调度循环跑起来后
	// 才有值，启动瞬间与测试里都拿不到。
	mu        sync.Mutex
	entries   map[int64]cron.EntryID
	schedules map[int64]cron.Schedule
}

// New 创建调度器。ctx 为常驻根 context，进程退出时取消。
func New(ctx context.Context, st *store.Store, runner Runner) *Scheduler {
	logger := slog.Default()
	return &Scheduler{
		store:     st,
		runner:    runner,
		ctx:       ctx,
		entries:   make(map[int64]cron.EntryID),
		schedules: make(map[int64]cron.Schedule),
		cron: cron.New(
			cron.WithChain(cron.Recover(slogCronLogger{logger})),
		),
	}
}

// slogCronLogger 把 cron 内部的 panic 恢复日志接到 slog（默认是标准库 log，不进日志文件）。
type slogCronLogger struct{ logger *slog.Logger }

func (l slogCronLogger) Info(msg string, keysAndValues ...any) {
	l.logger.Info(msg, keysAndValues...)
}
func (l slogCronLogger) Error(err error, msg string, keysAndValues ...any) {
	l.logger.Error(msg, append(keysAndValues, "error", err)...)
}

// Start 启动调度循环（不等任务，立即返回）。
func (s *Scheduler) Start() { s.cron.Start() }

// Stop 停止调度并等待正在执行的任务结束（受 ctx 取消驱动）。
func (s *Scheduler) Stop() context.Context { return s.cron.Stop() }

// Reload 从 DB 重新装载全部计划任务并重建调度表（增删改后调用，立即生效）。
func (s *Scheduler) Reload() error {
	tasks, err := s.store.ScheduledTasks()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entryID := range s.entries {
		s.cron.Remove(entryID)
	}
	s.entries = make(map[int64]cron.EntryID, len(tasks))
	s.schedules = make(map[int64]cron.Schedule, len(tasks))
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		if err := s.registerLocked(t); err != nil {
			// 表达式非法（如手工改库）不该拖垮整个调度：跳过并留日志，其余任务照常。
			slog.Warn("计划任务表达式非法，已跳过调度", "task_id", t.ID, "name", t.Name, "cron", t.Cron, "error", err)
		}
	}
	slog.Info("计划任务已装载", "total", len(tasks), "scheduled", len(s.entries))
	return nil
}

// registerLocked 注册单条任务，调用方需持有 s.mu。
func (s *Scheduler) registerLocked(t store.ScheduledTask) error {
	schedule, err := cron.ParseStandard(t.Cron)
	if err != nil {
		return err
	}
	task := toTask(t)
	entryID := s.cron.Schedule(schedule, cron.FuncJob(func() { s.fire(task) }))
	s.entries[t.ID] = entryID
	s.schedules[t.ID] = schedule
	return nil
}

// fire 到点执行：调 Runner 并把结果写回任务行。
func (s *Scheduler) fire(task Task) {
	start := time.Now()
	err := s.runner(s.ctx, task)
	status, message := StatusSuccess, ""
	switch {
	case err == nil:
	case errors.Is(err, ErrBusy):
		status, message = StatusSkipped, err.Error()
	default:
		status, message = StatusFailed, err.Error()
	}
	if rerr := s.store.RecordScheduledResult(task.ID, status, message); rerr != nil {
		slog.Warn("写入计划任务结果失败", "task_id", task.ID, "error", rerr)
	}
	slog.Info("计划任务执行结束", "task_id", task.ID, "name", task.Name, "type", task.Type,
		"status", status, "elapsed", time.Since(start).Round(time.Millisecond).String(), "message", message)
}

// RunNow 立即执行一次指定任务（同步；调用方需要异步请自行起 goroutine）。
// 与到点执行走同一路径，因此同样会更新「上次结果」。
func (s *Scheduler) RunNow(id int64) error {
	stored, err := s.store.ScheduledTask(id)
	if err != nil {
		return err
	}
	if err := Validate(stored.Cron); err != nil {
		return fmt.Errorf("cron 表达式非法: %w", err)
	}
	s.fire(toTask(stored))
	return nil
}

// Next 返回任务的下次执行时间；未调度（禁用/表达式非法）返回零值。
func (s *Scheduler) Next(id int64) time.Time {
	s.mu.Lock()
	schedule, ok := s.schedules[id]
	s.mu.Unlock()
	if !ok {
		return time.Time{}
	}
	return schedule.Next(time.Now())
}

// Preview 校验表达式并返回接下来 n 次执行时间（供管理端即时预览）。
func Preview(expr string, n int) ([]time.Time, error) {
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		n = 5
	}
	now := time.Now()
	out := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		now = schedule.Next(now)
		out = append(out, now)
	}
	return out, nil
}

// Validate 校验表达式是否合法。
func Validate(expr string) error {
	_, err := cron.ParseStandard(expr)
	return err
}

func toTask(stored store.ScheduledTask) Task {
	params := map[string]any{}
	if stored.Params != "" {
		_ = json.Unmarshal([]byte(stored.Params), &params)
	}
	return Task{ID: stored.ID, Name: stored.Name, Type: stored.Type, Cron: stored.Cron, Params: params}
}
