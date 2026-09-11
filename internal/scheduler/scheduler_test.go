package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func mustCreate(t *testing.T, st *store.Store, v store.ScheduledTask) store.ScheduledTask {
	t.Helper()
	task, err := st.CreateScheduledTask(v)
	if err != nil {
		t.Fatalf("创建计划任务: %v", err)
	}
	return task
}

// TestReloadSchedulesOnlyEnabled 验证禁用任务不进入调度表。
func TestReloadSchedulesOnlyEnabled(t *testing.T) {
	st := newTestStore(t)
	enabled := mustCreate(t, st, store.ScheduledTask{Name: "夜间扫库", Type: "scan", Cron: "0 3 * * *", Enabled: true})
	disabled := mustCreate(t, st, store.ScheduledTask{Name: "停用", Type: "scan", Cron: "0 4 * * *", Enabled: false})

	s := New(context.Background(), st, func(context.Context, Task) error { return nil })
	defer s.Stop()
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if s.Next(enabled.ID).IsZero() {
		t.Error("启用的任务应能查到下次执行时间")
	}
	if !s.Next(disabled.ID).IsZero() {
		t.Error("禁用的任务不应被调度")
	}
}

// TestReloadSkipsInvalidExpression 验证坏表达式只跳过该条，不影响其它任务。
func TestReloadSkipsInvalidExpression(t *testing.T) {
	st := newTestStore(t)
	// 直接写库绕过入口校验，模拟手工改库。
	bad := mustCreate(t, st, store.ScheduledTask{Name: "坏的", Type: "scan", Cron: "not a cron", Enabled: true})
	good := mustCreate(t, st, store.ScheduledTask{Name: "好的", Type: "scan", Cron: "@daily", Enabled: true})

	s := New(context.Background(), st, func(context.Context, Task) error { return nil })
	defer s.Stop()
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload 不应因单条非法而失败: %v", err)
	}
	if !s.Next(bad.ID).IsZero() {
		t.Error("非法表达式不应被调度")
	}
	if s.Next(good.ID).IsZero() {
		t.Error("合法任务应仍然被调度")
	}
}

// TestRunNowRecordsOutcome 验证三种结果都被写回任务行。
func TestRunNowRecordsOutcome(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status string
	}{
		{"成功", nil, StatusSuccess},
		{"失败", errors.New("boom"), StatusFailed},
		{"跳过", ErrBusy, StatusSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			task := mustCreate(t, st, store.ScheduledTask{Name: tc.name, Type: "scan", Cron: "0 3 * * *", Enabled: true})
			s := New(context.Background(), st, func(context.Context, Task) error { return tc.err })
			defer s.Stop()

			var got Task
			s.runner = func(_ context.Context, task Task) error { got = task; return tc.err }
			if err := s.RunNow(task.ID); err != nil {
				t.Fatalf("RunNow: %v", err)
			}
			if got.ID != task.ID || got.Cron != "0 3 * * *" {
				t.Errorf("Runner 收到的任务不对: %+v", got)
			}
			stored, err := st.ScheduledTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.LastStatus != tc.status {
				t.Errorf("last_status = %q，期望 %q", stored.LastStatus, tc.status)
			}
			if stored.LastRunAt == "" {
				t.Error("last_run_at 应被写入")
			}
		})
	}
}

// TestRunNowDisabledTask 验证禁用任务仍可手动立即执行。
func TestRunNowDisabledTask(t *testing.T) {
	st := newTestStore(t)
	task := mustCreate(t, st, store.ScheduledTask{Name: "停用", Type: "probe", Cron: "0 3 * * *", Enabled: false})
	var calls int32
	s := New(context.Background(), st, func(context.Context, Task) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	defer s.Stop()
	if err := s.RunNow(task.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if calls != 1 {
		t.Errorf("Runner 调用次数 = %d，期望 1", calls)
	}
}

// TestRunNowUnknown 验证不存在/表达式非法时返回错误。
func TestRunNowUnknown(t *testing.T) {
	st := newTestStore(t)
	s := New(context.Background(), st, func(context.Context, Task) error { return nil })
	defer s.Stop()
	if err := s.RunNow(999); err == nil {
		t.Error("不存在的任务应返回错误")
	}
	bad := mustCreate(t, st, store.ScheduledTask{Name: "坏的", Type: "scan", Cron: "nope", Enabled: true})
	if err := s.RunNow(bad.ID); err == nil {
		t.Error("非法表达式应返回错误")
	}
}

func TestPreviewAndValidate(t *testing.T) {
	if err := Validate("0 3 * * *"); err != nil {
		t.Errorf("标准五段表达式应合法: %v", err)
	}
	if err := Validate("@daily"); err != nil {
		t.Errorf("@daily 应合法: %v", err)
	}
	if err := Validate("61 * * * *"); err == nil {
		t.Error("分钟 61 应被拒绝")
	}
	if err := Validate("bad"); err == nil {
		t.Error("乱写的表达式应被拒绝")
	}

	runs, err := Preview("0 3 * * *", 3)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("预览条数 = %d，期望 3", len(runs))
	}
	for i, run := range runs {
		if !run.After(time.Now()) {
			t.Errorf("第 %d 次执行时间 %v 应在未来", i, run)
		}
		if i > 0 && !run.After(runs[i-1]) {
			t.Errorf("预览时间应递增: %v 不在 %v 之后", run, runs[i-1])
		}
		if hour := run.Hour(); hour != 3 {
			t.Errorf("0 3 * * * 应在 3 点执行，实际 %d 点", hour)
		}
	}
	if _, err := Preview("bad", 3); err == nil {
		t.Error("非法表达式应返回错误")
	}
}
