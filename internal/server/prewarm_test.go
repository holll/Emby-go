package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/imagecache"
	"emby-go/internal/store"
)

// newPrewarmTestApp 构造只够跑预热链路的 App：磁盘缓存真实、队列真实，
// 生成函数由用例注入。不走 newApp，让用例只有一个关注点。
func newPrewarmTestApp(t *testing.T) *App {
	t.Helper()
	disk, err := imagecache.Open(t.TempDir(), 64<<20, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		imgDisk:      disk,
		thumbSem:     make(chan struct{}, maxThumbConcurrency),
		thumbFlights: make(map[string]*thumbFlight),
		prewarmQueue: make(chan prewarmJob, prewarmQueueSize),
		tags:         make(map[string]tagEntry),
		rootCtx:      context.Background(),
	}
}

func writePoster(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "poster.jpg")
	// posterTag 只 stat，不读内容：任意字节都够用。
	if err := os.WriteFile(path, []byte("poster"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPrewarmThumbnailsFillsDiskCache 断言列表响应后投递的海报档位会被生成并落盘，
// 随后同一档位的请求直接命中磁盘缓存，不再调用生成函数。
func TestPrewarmThumbnailsFillsDiskCache(t *testing.T) {
	app := newPrewarmTestApp(t)
	poster := writePoster(t)
	var calls atomic.Int32
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		calls.Add(1)
		return []byte("webp-bytes"), nil
	}

	// 没有海报路径的影片不产生任务。
	app.prewarmThumbnails([]store.Movie{{ID: 1, PosterPath: poster}, {ID: 2}})
	if got := len(app.prewarmQueue); got != 1 {
		t.Fatalf("预热队列应有 1 条任务，实际 %d", got)
	}

	job := <-app.prewarmQueue
	if job.width != prewarmPosterWidth || job.height != 0 {
		t.Fatalf("预热档位 = %dx%d，期望 %dx0", job.width, job.height, prewarmPosterWidth)
	}
	app.prewarmOne(job)
	if got := calls.Load(); got != 1 {
		t.Fatalf("预热应生成 1 次，实际 %d", got)
	}

	// 浏览器随后按同一档位请求：应命中磁盘，不再生成。
	data, ok := app.thumbVariant(context.Background(), poster, job.tag, job.width, job.height, 0)
	if !ok || string(data) != "webp-bytes" {
		t.Fatalf("磁盘命中应返回预热字节，得到 ok=%v data=%q", ok, data)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("磁盘命中不应再生成，实际调用 %d 次", got)
	}
	if got := pendingFlights(app); got != 0 {
		t.Fatalf("预热结束后不应残留 flight，仍有 %d 条", got)
	}
}

// TestPrewarmThumbnailsSkipsWithoutDiskCache 断言磁盘缓存关闭时完全不投递任务：
// 生成了也没地方存，只会白烧 CPU。
func TestPrewarmThumbnailsSkipsWithoutDiskCache(t *testing.T) {
	app := newPrewarmTestApp(t)
	app.imgDisk = imagecache.Disabled()
	app.prewarmThumbnails([]store.Movie{{ID: 1, PosterPath: writePoster(t)}})
	if got := len(app.prewarmQueue); got != 0 {
		t.Fatalf("磁盘缓存关闭时不应投递预热任务，实际 %d 条", got)
	}
}

// TestPrewarmQueueDropsWhenFull 断言队列满时丢弃而不是阻塞：预热是优化，
// 不能因为刷页把列表响应卡住。
func TestPrewarmQueueDropsWhenFull(t *testing.T) {
	app := newPrewarmTestApp(t)
	poster := writePoster(t)
	movies := make([]store.Movie, 0, prewarmQueueSize*3)
	for i := range prewarmQueueSize * 3 {
		movies = append(movies, store.Movie{ID: int64(i + 1), PosterPath: poster})
	}
	app.prewarmThumbnails(movies)
	if got := len(app.prewarmQueue); got != prewarmQueueSize {
		t.Fatalf("队列应被填满到 %d 条，实际 %d", prewarmQueueSize, got)
	}
}

// TestPrewarmPanicDoesNotEscape 断言后台生成 panic 只丢弃这一条任务：
// worker 在独立 goroutine 里跑，没有 monitor 中间件兜底，panic 逃出去会带走整个进程。
func TestPrewarmPanicDoesNotEscape(t *testing.T) {
	app := newPrewarmTestApp(t)
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		panic("解码器崩溃")
	}
	app.prewarmOne(prewarmJob{path: writePoster(t), tag: "tag-panic", width: prewarmPosterWidth})
	if got := pendingFlights(app); got != 0 {
		t.Fatalf("panic 后 flight 表应已清空，仍有 %d 条", got)
	}
}

// TestPrewarmWorkerStopsOnShutdown 断言 worker 响应 rootCtx 取消并退出。
// 阻塞生成中的退出预算另由 TestWaitBackgroundIncludesPrewarm 覆盖。
func TestWaitBackgroundIncludesPrewarm(t *testing.T) {
	app := newShutdownApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	// 清理顺序先解除模拟 I/O 阻塞，再由 newShutdownApp 的清理收齐 worker。
	t.Cleanup(func() { close(release) })
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		close(started)
		<-release
		return []byte("webp-bytes"), nil
	}
	app.enqueuePrewarm(writePoster(t), prewarmPosterWidth, 0)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("预热生成未启动")
	}
	app.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := app.WaitBackground(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("生成仍在阻塞时必须耗尽关闭预算，得到 %v", err)
	}
	// 预算耗尽不会提前关库；实际主程序将以非零状态退出，而非进入 Close 无界等待。
	if _, err := app.db.Libraries(); err != nil {
		t.Fatalf("等待超时不应提前关库：%v", err)
	}
}

func TestPrewarmWorkerStopsOnShutdown(t *testing.T) {
	app := newPrewarmTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	app.rootCtx = ctx
	done := make(chan struct{})
	go func() {
		app.runPrewarmWorker()
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("rootCtx 取消后预热 worker 未退出")
	}
}
