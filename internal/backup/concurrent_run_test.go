package backup

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunConcurrentSameSecondPublishesOneCompleteBackup 验证同一输出目录、
// 同一秒启动的两路备份不会互相踩：只有一路成为完整备份，另一路明确报错。
//
// 旧实现里 stage 名只到秒，后启者一进来就 RemoveAll 掉先启者正在 VACUUM 的目录，
// 先启者随后还会把自己的半成品 rename 成正式目录——数据正确性问题，不能只靠
// 「两个 embybackup 不会同时跑」的部署假设。
func TestRunConcurrentSameSecondPublishesOneCompleteBackup(t *testing.T) {
	dbPath, _, outDir := fixture(t)
	now := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)

	var snapshots atomic.Int64 // 进入 VACUUM 的次数
	var hookCalls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	testHookBeforeSnapshot = func() { snapshots.Add(1) }
	testHookAfterSnapshot = func() {
		// 只挡住第一路：第二路若走到了这里，说明它已经在同一个 stage 上动手了。
		if hookCalls.Add(1) == 1 {
			enterOnce.Do(func() { close(entered) })
			<-release
		}
	}
	defer func() { testHookBeforeSnapshot, testHookAfterSnapshot = nil, nil }()

	type outcome struct {
		result Result
		err    error
	}
	first := make(chan outcome, 1)
	go func() {
		result, err := Run(Options{DBPath: dbPath, OutDir: outDir, Retain: 7, Now: now, NFORoot: true})
		first <- outcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("第一路没有走到发布前同步点")
	}

	// 第二路：同秒、同输出目录，NFORoot=false（一旦由它发布，产物就是「半份」）。
	second := make(chan outcome, 1)
	go func() {
		result, err := Run(Options{DBPath: dbPath, OutDir: outDir, Retain: 7, Now: now, NFORoot: false})
		second <- outcome{result: result, err: err}
	}()
	var secondRes outcome
	select {
	case secondRes = <-second:
	case <-time.After(20 * time.Second):
		t.Fatal("第二路既没失败也没返回：两路都在写同一个 stage")
	}
	if secondRes.err == nil {
		t.Errorf("同秒并发第二路竟然成功（dir=%s）：两路各自删掉对方的 stage 后互相覆盖", secondRes.result.Dir)
	} else if !errors.Is(secondRes.err, ErrOutputBusy) {
		t.Errorf("第二路错误 = %v，期望是 ErrOutputBusy（明确告诉调用方「已有一路在备份」）", secondRes.err)
	}
	if got := snapshots.Load(); got != 1 {
		t.Errorf("进入 VACUUM 的次数 = %d，期望 1（第二路不该动同一个 stage）", got)
	}

	unblock()
	var firstRes outcome
	select {
	case firstRes = <-first:
	case <-time.After(20 * time.Second):
		t.Fatal("第一路没有返回")
	}
	if firstRes.err != nil {
		t.Fatalf("第一路备份失败: %v", firstRes.err)
	}

	// 产物必须是「完整备份」：时间戳目录 + 完整库 + 全量 NFO，且没有 staging 残留。
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
			continue
		}
		t.Errorf("输出目录残留临时文件 %q（锁文件/半成品必须清理）", entry.Name())
	}
	if len(dirs) != 1 || dirs[0] != "20261018T020000Z" {
		t.Fatalf("输出目录内容 = %v，期望只有一份 20261018T020000Z", dirs)
	}
	if firstRes.result.NFOCount != 2 {
		t.Errorf("NFOCount = %d，期望 2", firstRes.result.NFOCount)
	}
	for _, name := range []string{"db.sqlite", filepath.Join("nfo", "lib-1", "a.nfo"), filepath.Join("nfo", "lib-1", "sub", "b.nfo")} {
		if _, err := os.Stat(filepath.Join(firstRes.result.Dir, name)); err != nil {
			t.Errorf("完整备份缺少 %s: %v", name, err)
		}
	}
	if err := checkSnapshot(firstRes.result.Database); err != nil {
		t.Errorf("发布出来的库不完整: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, lockFileName)); !os.IsNotExist(err) {
		t.Errorf("备份结束后锁文件仍在（err=%v）", err)
	}
}
