package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
)

// 本文件覆盖 §1.1 P1-2 的关停窗口回归：登记门（beginWorker）与
// workerWG.Wait 互斥、5 个缺门入口在关停后回 503、Close 等 worker 收齐。

// TestBeginWorkerRejectsAfterShutdown 断言 BeginShutdown 之后登记一律失败，
// 且没有留下未归还的计数（否则 Close/joinBackground 会永远等下去）。
func TestBeginWorkerRejectsAfterShutdown(t *testing.T) {
	app := newShutdownApp(t)
	if !app.workerAdmissionOpen() {
		t.Fatal("启动后登记门应处于打开状态")
	}

	app.BeginShutdown()
	app.BeginShutdown()       // 幂等
	app.stopWorkerAdmission() // 幂等：重复关门不应 panic 或翻转状态

	if app.workerAdmissionOpen() {
		t.Fatal("BeginShutdown 后登记门仍开着")
	}
	release, err := app.beginWorker()
	if !errors.Is(err, errServerShuttingDown) {
		t.Fatalf("关停后登记应返回 errServerShuttingDown，得到 %v", err)
	}
	if release != nil {
		t.Fatal("登记失败时不应返回释放函数")
	}

	// 计数未增长：Wait 立即返回（被拒绝的登记不会让 workerWG 停在非零）。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := app.joinBackground(ctx); err != nil {
		t.Fatalf("joinBackground: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("关停后 joinBackground 应立刻返回，实际 %s", elapsed)
	}
}

// TestBeginWorkerConcurrentWithWait 制造「Add 与 Wait 并发」的原始 panic 场景：
// 多个 goroutine 不停 beginWorker()+release()，同时主 goroutine 关门并 Wait。
// 未加登记门时这里会 panic「sync: WaitGroup misuse: Add called concurrently with Wait」，
// 或让 Wait 在计数为 0 时提前返回；加门后所有登记要么发生在 Wait 之前、
// 要么被拒绝，因此 -race 下干净且 Wait 一定是最后返回的那个。
func TestBeginWorkerConcurrentWithWait(t *testing.T) {
	app := newShutdownApp(t)

	const writers = 8
	var wg sync.WaitGroup
	var accepted, rejected atomic.Int64
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				release, err := app.beginWorker()
				if err != nil {
					// 登记门已关闭：Wait 已经开始，不再 Add。
					rejected.Add(1)
					return
				}
				accepted.Add(1)
				release()
			}
		}()
	}

	close(start)
	// 让 writer 先跑一会儿，保证 BeginShutdown（关门）落在它们中间。
	time.Sleep(20 * time.Millisecond)
	app.BeginShutdown()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- app.WaitBackground(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitBackground 不应超时或报错：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitBackground 挂死")
	}
	wg.Wait()

	if accepted.Load() == 0 {
		t.Fatal("竞争期间应至少成功登记过一次（否则用例没有制造出 Add/Wait 竞争）")
	}
	if rejected.Load() == 0 {
		t.Fatal("关停后应至少有一次登记被拒绝")
	}
	if release, err := app.beginWorker(); err == nil {
		release()
		t.Fatal("Wait 结束后仍能登记 worker")
	}
}

// TestShutdownEntryPointsReturn503 断言 5 个原本缺根 ctx 门的入口在关停后
// 直接 503，而不是继续进真实逻辑（读库、占 NFO 通道、登记新 worker）。
// 用 newTranslationFixture 提供 admin 令牌 + 媒体库/影片，全部走进程内 router。
func TestShutdownEntryPointsReturn503(t *testing.T) {
	f := newTranslationFixture(t)
	// 立即执行计划任务需要一行真实任务；直接落库，避免依赖 cron 校验。
	task, err := f.a.db.CreateScheduledTask(store.ScheduledTask{
		Name: "shutdown-gate", Type: "probe", Cron: "* * * * *", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateScheduledTask: %v", err)
	}

	f.a.BeginShutdown()

	entries := []struct{ name, method, path, body string }{
		{"probe_media", http.MethodPost, "/api/admin/probe/media", `{"library_id":0}`},
		{"scrape_run", http.MethodPost, "/api/admin/scrape/run", `{}`},
		{"scrape_avatars", http.MethodPost, "/api/admin/scrape/avatars", `{}`},
		{"tasks_scan", http.MethodPost, "/api/admin/tasks/scan", ""},
		{"scheduled_run", http.MethodPost, fmt.Sprintf("/api/admin/scheduled/%d/run", task.ID), ""},
	}
	for _, entry := range entries {
		w := translationCall(f.a, f.token, entry.method, entry.path, entry.body)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: 关停后应回 503，得到 %d %s", entry.name, w.Code, w.Body.String())
		}
	}

	// 没有任何入口登记新 worker：Wait 必须立即返回。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := f.a.joinBackground(ctx); err != nil {
		t.Fatalf("joinBackground: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("关停后入口仍登记了 worker：joinBackground 耗时 %s", elapsed)
	}
}

// TestCloseWaitsForWorkers 断言 Close 在 workerWG 收齐后才关库（P2-15）。
// 顺序用 channel 断言：Close 返回时 worker 的完成信号必须已经关闭。
func TestCloseWaitsForWorkers(t *testing.T) {
	app := newShutdownApp(t)
	release, err := app.beginWorker()
	if err != nil {
		t.Fatalf("beginWorker: %v", err)
	}
	finished := make(chan struct{})
	go func() {
		// worker 必须响应 rootCtx 取消才会被 Wait 收齐；这里用固定 sleep 模拟
		// 「收到取消后还需要一点收尾时间」的 worker。
		time.Sleep(80 * time.Millisecond)
		close(finished)
		release()
	}()

	app.BeginShutdown()
	closed := make(chan struct{})
	go func() {
		app.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 挂死（worker 未收齐或 joinBackground 无界等待出错）")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close 在 worker 收齐前就返回了：关库可能与仍在跑的 worker 相撞")
	}
	if app.workerAdmissionOpen() {
		t.Fatal("Close 之后登记门应保持关闭")
	}
}
