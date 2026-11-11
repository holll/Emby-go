package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/scheduler"
	"emby-go/internal/store"
)

// captureSpriteLogs 把 slog 输出收进内存，返回读取已捕获日志的闭包。
// handler 复用 mediainfo_test.go:684 的 captureHandler，不重复定义。
func captureSpriteLogs(t *testing.T) func() string {
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

// spriteTaskFor 构造一条雪碧图计划任务的运行期视图。
func spriteTaskFor(lib int64, params map[string]any) scheduler.Task {
	return scheduler.Task{ID: 41, Name: "夜间雪碧图", Type: "sprites", Cron: "@daily", Params: params}
}

// TestScheduledSpritesFailureLogsCause 对应用户反馈：任务日志只有「已开始/已结束」，
// 看不到雪碧图为什么生成失败。逐影片失败必须留下影片 id 与底层原因。
// 同时锁住边界：原始原因只进服务日志，不能落进管理端可见的任务行/上次结果
// （错误文本可能含带凭据的 STRM URL，见 TestScheduledSpritesSchedulerOutcomes 的 canary）。
func TestScheduledSpritesFailureLogsCause(t *testing.T) {
	f := artworkFixture(t)
	logs := captureSpriteLogs(t)
	var seen int64
	f.a.sprites.run = func(_ context.Context, _ *store.Store, r artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		seen = r.MovieID
		return artwork.SpriteResult{}, errors.New("canary-ffmpeg-exit-1")
	}
	err := f.a.runScheduledSprites(context.Background(), spriteTaskFor(f.lib, map[string]any{"library_id": f.lib, "limit": 1}))
	if err == nil {
		t.Fatal("失败必须返回错误")
	}
	if seen != f.movie {
		t.Fatalf("fake run 未收到目标影片: %d", seen)
	}
	joined := logs()
	for _, want := range []string{
		"视频预览生成失败",
		"movie_id=" + fmt.Sprint(f.movie),
		"canary-ffmpeg-exit-1",
		"视频预览任务结束：部分影片生成失败",
		"first_error=",
		"suppressed=0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("失败日志缺少 %q:\n%s", want, joined)
		}
	}
	s := spriteJoinStatus(f.a)
	if s.Failed != 1 || s.Done != 1 || s.Running {
		t.Fatalf("status %+v", s)
	}
	// 管理端文案保持不变，且不得泄漏原始原因。
	if s.Error != "部分视频预览生成失败，请检查可信源及服务配置" {
		t.Fatalf("status error %q", s.Error)
	}
	if strings.Contains(s.Error, "canary") {
		t.Fatalf("原始原因泄漏到管理端状态: %q", s.Error)
	}
	task := f.a.tasks[0]
	if task.Status != "failed" || !strings.Contains(task.Error, "部分视频预览生成失败") || strings.Contains(task.Error, "canary") {
		t.Fatalf("task %+v", task)
	}
}

// TestScheduledSpritesFailureLogLimit 逐影片日志必须限量：大规模失败时只记前
// spriteFailLogLimit 条原始原因，其余用汇总行的 suppressed 计数，避免刷屏。
func TestScheduledSpritesFailureLogLimit(t *testing.T) {
	f := artworkFixture(t)
	for i := range 11 {
		if _, err := f.a.db.IndexMovie(store.Movie{LibraryID: f.lib, SourcePath: fmt.Sprintf("/fixture/log-%d.strm", i), Title: fmt.Sprint(i), Status: "success"}, 1, time.Now(), nil); err != nil {
			t.Fatal(err)
		}
	}
	logs := captureSpriteLogs(t)
	calls := 0
	f.a.sprites.run = func(context.Context, *store.Store, artwork.SpriteRequest, artwork.SpriteOptions) (artwork.SpriteResult, error) {
		calls++
		return artwork.SpriteResult{}, errors.New("canary-batch-failure")
	}
	if err := f.a.runScheduledSprites(context.Background(), spriteTaskFor(f.lib, map[string]any{"library_id": f.lib})); err == nil {
		t.Fatal("失败必须返回错误")
	}
	if calls != 12 {
		t.Fatalf("目标数 %d", calls)
	}
	joined := logs()
	// 只数逐影片行（带 movie_id 属性）：聚合行里「部分视频预览生成失败」也含同样的词。
	if got := strings.Count(joined, "movie_id="); got != spriteFailLogLimit {
		t.Fatalf("逐影片失败日志 %d 条，期望 %d:\n%s", got, spriteFailLogLimit, joined)
	}
	if !strings.Contains(joined, "failed=12") || !strings.Contains(joined, "suppressed=2") || !strings.Contains(joined, "logged=10") {
		t.Fatalf("汇总行缺少计数:\n%s", joined)
	}
	if s := spriteJoinStatus(f.a); s.Failed != 12 || s.Done != 12 {
		t.Fatalf("status %+v", s)
	}
}

// TestScheduledSpritesTargetReadFailureKeepsCause 目标分页读取失败曾经只写
// 固定文案「视频预览任务目标读取失败」，底层 err 被吞掉；现在必须同时出现在
// 任务行、返回值和汇总日志里。
func TestScheduledSpritesTargetReadFailureKeepsCause(t *testing.T) {
	f := artworkFixture(t)
	logs := captureSpriteLogs(t)
	id := f.a.startTask("video_sprites")
	if !f.a.claimNFO("sprites") {
		t.Fatal("claim")
	}
	f.a.sprites.mu.Lock()
	f.a.sprites.status = spriteStatus{InstanceID: f.a.instanceID, TaskID: id, MovieIDs: []int64{}, Running: true}
	f.a.sprites.mu.Unlock()
	req := spriteRequest{next: func(context.Context) (int64, int, bool, error) {
		return 0, 0, false, errors.New("canary-target-read")
	}}
	err := f.a.executeSprites(context.Background(), id, req, artwork.SpriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "canary-target-read") {
		t.Fatalf("返回值丢了底层原因: %v", err)
	}
	if s := spriteJoinStatus(f.a); !strings.Contains(s.Error, "视频预览任务目标读取失败") || !strings.Contains(s.Error, "canary-target-read") {
		t.Fatalf("status error %q", s.Error)
	}
	task := f.a.tasks[0]
	if task.Status != "failed" || !strings.Contains(task.Error, "canary-target-read") {
		t.Fatalf("task %+v", task)
	}
	joined := logs()
	if !strings.Contains(joined, "视频预览任务中止：目标读取失败") || !strings.Contains(joined, "canary-target-read") {
		t.Fatalf("日志缺少原因:\n%s", joined)
	}
}

// TestScheduledSpritesSetupFailureLogsCause 覆盖 startTask 之前的失败：参数无效、
// ffmpeg/ffprobe 不可用、媒体库范围不可用。这些路径以前既没有任务条目，也没有
// 底层原因，只能看到一句固定文案。
func TestScheduledSpritesSetupFailureLogsCause(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		cause string
		prep  func(f translationFixture)
		param map[string]any
	}{
		{
			name:  "params",
			stage: "参数无效",
			cause: "unknown sprites parameter",
			param: map[string]any{"movie_ids": []int{1}},
		},
		{
			name:  "ffmpeg",
			stage: "ffmpeg 或 ffprobe 不可用",
			cause: "ffmpeg 不可用",
			prep: func(f translationFixture) {
				f.a.artwork.lookPath = func(string) (string, error) { return "", errors.New("canary-lookpath") }
			},
			param: map[string]any{"library_id": 1},
		},
		{
			name:  "scope",
			stage: "视频预览媒体库范围不可用",
			cause: "database is closed",
			prep:  func(f translationFixture) { _ = f.a.db.Close() },
			param: map[string]any{"library_id": 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := artworkFixture(t)
			if tc.prep != nil {
				tc.prep(f)
			}
			logs := captureSpriteLogs(t)
			err := f.a.runScheduledSprites(context.Background(), spriteTaskFor(f.lib, tc.param))
			if err == nil {
				t.Fatal("失败必须返回错误")
			}
			if !strings.Contains(err.Error(), tc.stage) || !strings.Contains(err.Error(), tc.cause) {
				t.Fatalf("返回值丢了阶段或原因: %v", err)
			}
			joined := logs()
			if !strings.Contains(joined, "雪碧图计划任务失败") || !strings.Contains(joined, "夜间雪碧图") ||
				!strings.Contains(joined, tc.stage) || !strings.Contains(joined, tc.cause) {
				t.Fatalf("日志缺少任务名/阶段/原因:\n%s", joined)
			}
			// 任务页要能看到这次执行，而不是一片空白。
			if len(f.a.tasks) != 1 {
				t.Fatalf("任务列表没有条目: %+v", f.a.tasks)
			}
			if task := f.a.tasks[0]; task.Status != "failed" || !strings.Contains(task.Error, tc.cause) || task.Type != "video_sprites" {
				t.Fatalf("task %+v", task)
			}
		})
	}
}

// TestScheduledSpritesBusyReason 复核跳过路径：上一轮仍在执行与别的媒体维护
// 任务占用 NFO 锁必须能区分，两者运维处置完全不同。
// Use actual subprocesses, not a synthetic error, to prove that detailed safe
// diagnostics reach failure logs but neither source secrets nor argv reach DTOs.
func TestScheduledSpritesSafeProcessDiagnostics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	for _, stage := range []string{"probe", "frame"} {
		t.Run(stage, func(t *testing.T) {
			f := artworkFixture(t)
			movie, err := f.a.db.Movie(f.movie)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(movie.SourcePath, []byte("https://secret-host-canary.invalid/private-path-canary?token=query-canary"), 0600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			probe, ffmpeg := filepath.Join(dir, "ffprobe"), filepath.Join(dir, "ffmpeg")
			bad := "#!/bin/sh\nprintf '%s\\n' \"$@\" 'standalone-token-canary HTTP error 403 Forbidden' >&2\nexit 7\n"
			// Two frames at the 10s interval: extraction is serial, so frame 0
			// always fails first and the reported position is deterministic.
			probeScript := "#!/bin/sh\nprintf '20\\n'\n"
			if stage == "probe" {
				probeScript = bad
			}
			if err := os.WriteFile(probe, []byte(probeScript), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ffmpeg, []byte(bad), 0700); err != nil {
				t.Fatal(err)
			}
			f.a.artwork.lookPath = func(name string) (string, error) {
				if name == "ffmpeg" {
					return ffmpeg, nil
				}
				return probe, nil
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			if err := f.a.runScheduledSprites(context.Background(), spriteTaskFor(f.lib, map[string]any{"library_id": f.lib})); err == nil {
				t.Fatal("expected subprocess failure")
			}
			joined := logs.String()
			for _, want := range []string{"media_process", "argv_template", "$STREAM_URL", "http error 403", "exit_code\":7", "stage=" + stage, "total_budget_ms=" + strconv.Itoa(int(artwork.SpriteTimeoutDefault.Milliseconds())), "stage_budget_ms="} {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in %s", want, joined)
				}
			}
			if stage == "frame" && (!strings.Contains(joined, "frame_index=0") || !strings.Contains(joined, "seek_ms=0")) {
				t.Errorf("missing frame position: %s", joined)
			}
			for _, secret := range []string{"secret-host-canary", "private-path-canary", "query-canary", "standalone-token-canary"} {
				if strings.Contains(joined, secret) {
					t.Errorf("secret leaked: %q", secret)
				}
			}
			for _, path := range []string{"/api/admin/tasks", "/api/admin/sprites/status"} {
				response := translationCall(f.a, f.token, "GET", path, "")
				if response.Code != 200 {
					t.Fatalf("%s status %d: %s", path, response.Code, response.Body.String())
				}
				for _, forbidden := range []string{"canary", "argv", "stderr", "$STREAM_URL", "http error 403"} {
					if strings.Contains(response.Body.String(), forbidden) {
						t.Errorf("DTO leaked %s: %s", forbidden, response.Body.String())
					}
				}
			}
		})
	}
}

func TestScheduledSpritesBusyReason(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		f := artworkFixture(t)
		logs := captureSpriteLogs(t)
		f.a.sprites.mu.Lock()
		f.a.sprites.status.Running = true
		f.a.sprites.mu.Unlock()
		err := f.a.runScheduledSprites(context.Background(), spriteTaskFor(f.lib, map[string]any{"library_id": f.lib}))
		if !errors.Is(err, scheduler.ErrBusy) || !strings.Contains(err.Error(), "上一轮雪碧图生成仍在执行") {
			t.Fatalf("busy err %v", err)
		}
		if joined := logs(); !strings.Contains(joined, "雪碧图计划任务跳过：上一轮生成仍在执行") {
			t.Fatalf("跳过日志不可定位:\n%s", joined)
		}
	})
	t.Run("nfo", func(t *testing.T) {
		f := artworkFixture(t)
		logs := captureSpriteLogs(t)
		if !f.a.claimNFO("scrape") {
			t.Fatal("claim")
		}
		defer f.a.releaseNFO("scrape")
		err := f.a.runScheduledSprites(context.Background(), spriteTaskFor(f.lib, map[string]any{"library_id": f.lib}))
		if !errors.Is(err, scheduler.ErrBusy) || !strings.Contains(err.Error(), "刮削或翻译等媒体维护任务正在占用") {
			t.Fatalf("busy err %v", err)
		}
		if joined := logs(); !strings.Contains(joined, "雪碧图计划任务跳过：刮削/翻译等媒体维护任务正在占用") {
			t.Fatalf("跳过日志不可定位:\n%s", joined)
		}
	})
}
