package server

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/scheduler"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

// 计划任务日志的验收点：每一类任务都要能回答「跑了哪个范围、成功/跳过/失败各多少、
// 花了多久、第一个失败是什么、没跑是被谁占着」。
// 这些断言都落在结构化字段上，删掉对应字段即失败（见报告里的变异记录）。

// captureLogs 接管 slog 默认 logger，返回读取全部日志行的函数。
// captureHandler 来自 mediainfo_test.go（同 package 复用，避免第二份实现漂移）。
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	previous := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{mu: &mu, lines: &lines}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

// newScheduledLogApp 建一个空库应用：这些用例只关心日志与状态机，不需要真实媒体文件。
func newScheduledLogApp(t *testing.T) *App {
	t.Helper()
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "test.db")}, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.WaitBackground(ctx)
		a.Close()
	})
	return a
}

// configureScrape 打开 MetaTube 与翻译开关：prepare* 阶段的前置条件。
func configureScrape(t *testing.T, a *App) {
	t.Helper()
	for key, value := range map[string]string{
		settingMetatubeURL:     "http://127.0.0.1:1",
		settingMetatubeToken:   "test-token",
		settingTranslateAPIURL: "http://127.0.0.1:1",
		settingTranslateAPIKey: "test-key",
	} {
		if err := a.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScheduledScopeRendersStableScope(t *testing.T) {
	got := scheduledScope(map[string]any{"only_missing": true, "library_id": 2, "limit": 50})
	if want := "library_id=2 limit=50 only_missing=true"; got != want {
		t.Fatalf("作用域渲染 = %q，期望 %q（键必须稳定排序）", got, want)
	}
	if got := scheduledScope(nil); got != "全部范围" {
		t.Fatalf("空参数 = %q，期望 全部范围", got)
	}
	if got := scheduledScope(map[string]any{"library_id": nil}); got != "全部范围" {
		t.Fatalf("全空值 = %q，期望 全部范围", got)
	}
}

func TestScheduledScanSkipsWithReasonAndScope(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)

	a.scanMu.Lock()
	a.scanStatus.Running = true
	a.scanMu.Unlock()
	err := a.runScheduledScan(scheduler.Task{ID: 7, Name: "夜间扫库", Type: "scan",
		Params: map[string]any{"library_id": float64(3), "mode": "incremental"}})
	if !errors.Is(err, scheduler.ErrBusy) {
		t.Fatalf("忙时 err = %v，期望 ErrBusy", err)
	}
	joined := logs()
	for _, want := range []string{"计划任务扫描跳过", "scope=library_id=3 mode=incremental",
		"reason=扫库/重建索引任务正在进行"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
	if !strings.Contains(err.Error(), "扫库/重建索引任务正在进行") {
		t.Fatalf("ErrBusy 应带上占用原因，得到 %q", err.Error())
	}
}

func TestScheduledScanParameterErrorKeepsLogAndCause(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)
	err := a.runScheduledScan(scheduler.Task{ID: 8, Name: "坏参数扫库", Type: "scan",
		Params: map[string]any{"mode": "fast"}})
	if err == nil {
		t.Fatal("非法 mode 应失败")
	}
	joined := logs()
	for _, want := range []string{"计划任务扫描参数非法，本次未执行", "error=",
		"scan mode must be full or incremental"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestScheduledProbeSkipLogsScopeAndReason(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)

	// 空库：走「没有可探测的影片」的跳过分支。
	if err := a.runScheduledProbe(scheduler.Task{ID: 9, Name: "夜间探测", Type: "probe",
		Params: map[string]any{"library_id": float64(2)}}); err != nil {
		t.Fatalf("空库跳过不应报错: %v", err)
	}
	joined := logs()
	for _, want := range []string{"计划任务探测跳过：没有可探测的影片", "scope=library_id=2",
		"status=success"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}

	// 忙：跳过原因必须说清是被什么占着，而不是一句空泛的「任务正在进行中」。
	logs = captureLogs(t)
	a.probeTaskMu.Lock()
	a.probeStatus.Running = true
	a.probeTaskMu.Unlock()
	err := a.runScheduledProbe(scheduler.Task{ID: 10, Name: "夜间探测", Type: "probe"})
	if !errors.Is(err, scheduler.ErrBusy) {
		t.Fatalf("忙时 err = %v，期望 ErrBusy", err)
	}
	joined = logs()
	for _, want := range []string{"计划任务探测跳过", "reason=同类探测任务或 NFO 通道已被占用"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestScheduledScrapeSkipLogsOwnerAndScope(t *testing.T) {
	a := newScheduledLogApp(t)
	task := scheduler.Task{ID: 11, Name: "夜间刮削", Type: "scrape",
		Params: map[string]any{"library_id": float64(4), "limit": float64(20)}}

	// 未配置：日志要留下原因，而不是只让任务行背锅。
	logs := captureLogs(t)
	if err := a.runScheduledScrape(task); err == nil || !strings.Contains(err.Error(), "未配置 MetaTube") {
		t.Fatalf("未配置时 err = %v", err)
	}
	joined := logs()
	for _, want := range []string{"计划任务刮削未执行：未配置 MetaTube 地址或 token",
		"scope=library_id=4 limit=20"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}

	// 已配置但 NFO 通道被探测占着：跳过原因要指名占用者。
	configureScrape(t, a)
	if !a.claimNFO("probe") {
		t.Fatal("claim probe")
	}
	defer a.releaseNFO("probe")
	logs = captureLogs(t)
	err := a.runScheduledScrape(task)
	if !errors.Is(err, scheduler.ErrBusy) {
		t.Fatalf("NFO 被占用时 err = %v，期望 ErrBusy", err)
	}
	if !strings.Contains(err.Error(), "媒体信息探测 正在占用 NFO 写入通道") {
		t.Fatalf("ErrBusy 应指名占用者，得到 %q", err.Error())
	}
	joined = logs()
	for _, want := range []string{"计划任务刮削跳过", "reason=媒体信息探测 正在占用 NFO 写入通道"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestScheduledScrapeAvatarsSkipLogsScope(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)
	if err := a.runScheduledScrapeAvatars(scheduler.Task{ID: 12, Name: "补头像", Type: "scrape_avatars",
		Params: map[string]any{"library_id": float64(5), "limit": float64(9)}}); err == nil {
		t.Fatal("未配置时应失败")
	}
	joined := logs()
	for _, want := range []string{"计划任务头像补全未执行：未配置 MetaTube 地址或 token",
		"scope=library_id=5 limit=9"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestExecuteScrapeEndLogCarriesScopeCountsElapsedAndFirstFailure(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)
	configureScrape(t, a)
	// 预置一条失败样例：验证结束日志把「失败首因」一并带出，
	// 不必再去翻 /scrape/progress 才知道失败长什么样。
	a.addScrapeFailure("失败 示例片：上游返回 500")

	err := a.executeScrape(a.scrapeConfig(), scraper.RunOptions{
		LibraryID: 6, Limit: 15, OnlyMissing: true, Overwrite: false,
	})
	if err != nil {
		t.Fatalf("空库刮削不应报错: %v", err)
	}
	joined := logs()
	for _, want := range []string{"刮削任务结束", "scope=library_id=6 limit=15 only_missing=true overwrite=false",
		"success=0", "failed=0", "total=0", "elapsed=", "first_failure=失败 示例片：上游返回 500"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestExecuteScrapeAvatarsEndLogCarriesScopeAndElapsed(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)
	configureScrape(t, a)
	// 与影片刮削同源：结束日志要能带出失败首因，不再要求运维去翻进度接口。
	a.addScrapeFailure("演员 某演员：上游头像 404")
	if err := a.executeScrapeAvatars(a.scrapeConfig(), 7, 11); err != nil {
		t.Fatalf("空库补头像不应报错: %v", err)
	}
	joined := logs()
	for _, want := range []string{"演员头像任务结束", "scope=library_id=7 limit=11",
		"success=0", "total=0", "elapsed=", "first_failure=演员 某演员：上游头像 404"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestScheduledTranslationLogsSummaryWithFirstFailure(t *testing.T) {
	f := newTranslationFixture(t)
	logs := captureLogs(t)
	// 逐片接缝：让唯一一部影片失败，验证汇总日志带 failed 计数与失败首因。
	boom := errors.New("上游翻译返回 502")
	f.a.translation.item = func(context.Context, store.Movie, scraper.TranslationOptions) (scraper.TranslationResult, error) {
		return scraper.TranslationResult{}, boom
	}
	err := f.a.runScheduledTranslation(scheduler.Task{ID: 13, Name: "夜间翻译", Type: "translate",
		Params: map[string]any{"library_id": float64(f.lib), "limit": float64(5)}})
	if err == nil || !strings.Contains(err.Error(), "部分影片翻译失败") {
		t.Fatalf("翻译失败应返回明确错误，得到 %v", err)
	}
	joined := logs()
	for _, want := range []string{"计划任务翻译开始", "scope=library_id=", "独立翻译任务结束",
		"failed=1", "first_failure=", "上游翻译返回 502", "elapsed="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestScheduledTranslationSkipLogsReason(t *testing.T) {
	f := newTranslationFixture(t)
	if !f.a.claimNFO("edit") {
		t.Fatal("claim edit")
	}
	defer f.a.releaseNFO("edit")
	logs := captureLogs(t)
	err := f.a.runScheduledTranslation(scheduler.Task{ID: 14, Name: "夜间翻译", Type: "translate"})
	if !errors.Is(err, scheduler.ErrBusy) {
		t.Fatalf("NFO 被占用时 err = %v，期望 ErrBusy", err)
	}
	joined := logs()
	for _, want := range []string{"计划任务翻译跳过", "reason=翻译或其它 NFO 写入任务正在进行"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}

func TestScheduledScanSuccessLogsCountsAndElapsed(t *testing.T) {
	logs := captureLogs(t)
	a := newScheduledLogApp(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Test Movie (2020).strm"),
		[]byte("https://example.com/video.mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := a.db.AddLibrary("ScheduledLog", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.runScheduledScan(scheduler.Task{ID: 15, Name: "夜间扫库", Type: "scan",
		Params: map[string]any{"library_id": float64(lib.ID), "mode": "full"}}); err != nil {
		t.Fatalf("扫描应成功: %v", err)
	}
	joined := logs()
	for _, want := range []string{"计划任务扫描开始", "scope=library_id=", "mode=full",
		"计划任务扫描完成", "elapsed=", "success=", "failed=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q，日志:\n%s", want, joined)
		}
	}
}
