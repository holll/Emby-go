package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/cache"
	"emby-go/internal/imagecache"
)

// newThumbTestApp 构造只够跑缩略图链路的 App：内存 LRU 真实、磁盘缓存禁用、
// thumbGenerate 由用例注入。不走 newApp，是为了让这些用例只有一个关注点。
func newThumbTestApp() *App {
	return &App{
		imgThumb:     cache.NewMemory(8),
		imgDisk:      imagecache.Disabled(),
		thumbSem:     make(chan struct{}, maxThumbConcurrency),
		thumbFlights: make(map[string]*thumbFlight),
	}
}

// thumbTestContext 造一个带可取消 ctx 的 gin.Context（thumbnail 会用 requests ctx）。
// 不调 gin.SetMode：它在并发用例里是包级全局写，-race 下会误报。
func thumbTestContext(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c, recorder
}

// pendingFlights 返回当前登记中的 flight 数（用于断言没有残留）。
func pendingFlights(a *App) int {
	a.thumbMu.Lock()
	defer a.thumbMu.Unlock()
	return len(a.thumbFlights)
}

// TestThumbnailLeaderPanicReleasesFlight 覆盖 P1-3 (b)：leader 生成期间 panic 时
// 必须先注销 flight 再把 panic 抛出去，否则该 key 之后所有请求都会卡在 <-flight.done。
func TestThumbnailLeaderPanicReleasesFlight(t *testing.T) {
	app := newThumbTestApp()
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		panic("编码器内部崩溃")
	}
	c, _ := thumbTestContext(t, "/Items/1/Images/Primary?maxWidth=50")

	// panic 必须继续向上抛（保留 monitor 中间件对异常的记录语义）。
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("leader panic 必须继续向上抛出，不能被吞掉")
			}
		}()
		if _, ok := app.thumbnail(c, "/library/poster.jpg", "tag-panic"); ok {
			t.Fatal("panic 路径不应声称生成成功")
		}
	}()

	if got := pendingFlights(app); got != 0 {
		t.Fatalf("panic 后 flight 表必须清空，仍有 %d 条残留", got)
	}

	// 同一 key 的下一次请求必须能正常走完 leader 流程（而不是等一个永不关闭的 done）。
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		return []byte("fake-webp-bytes"), nil
	}
	c2, _ := thumbTestContext(t, "/Items/1/Images/Primary?maxWidth=50")
	data, ok := app.thumbnail(c2, "/library/poster.jpg", "tag-panic")
	if !ok || len(data) == 0 {
		t.Fatalf("panic 之后同 key 应可再次生成，得到 ok=%v len=%d", ok, len(data))
	}
	if got := pendingFlights(app); got != 0 {
		t.Fatalf("成功路径也不应残留 flight，仍有 %d 条", got)
	}
}

// TestThumbnailLeaderPanicUnblocksFollower 断言 follower 拿到的是「失败」而不是永久阻塞：
// leader panic 注销时带 ok=false，等待者立刻回退原图。
func TestThumbnailLeaderPanicUnblocksFollower(t *testing.T) {
	app := newThumbTestApp()
	entered := make(chan struct{})
	release := make(chan struct{})
	var started sync.Once
	var mu sync.Mutex
	calls := 0
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		started.Do(func() { close(entered) })
		<-release // 让 leader 停在生成中，好让 follower 有机会登记成等待者
		panic("解码器崩溃")
	}

	leaderCtx, _ := thumbTestContext(t, "/Items/2/Images/Primary?maxWidth=50")
	go func() {
		defer func() { _ = recover() }() // 生产代码里这一层由 monitor 中间件记录
		_, _ = app.thumbnail(leaderCtx, "/library/landscape.jpg", "tag-follower")
	}()
	<-entered
	if got := pendingFlights(app); got != 1 {
		t.Fatalf("leader 生成中应有 1 条 flight，实际 %d", got)
	}

	// follower：此时 flight 已被 leader 占用，它只能等 done。
	followerCtx, _ := thumbTestContext(t, "/Items/2/Images/Primary?maxWidth=50")
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- false // 万一它变成了新 leader 并 panic，也算「没有永久阻塞」
			}
		}()
		_, ok := app.thumbnail(followerCtx, "/library/landscape.jpg", "tag-follower")
		done <- ok
	}()
	select {
	case ok := <-done:
		t.Fatalf("follower 在 leader 还没结束时就返回了（ok=%v）", ok)
	case <-time.After(100 * time.Millisecond):
	}
	close(release) // leader 崩溃 → 必须注销 flight 并把 ok=false 发布给 follower

	select {
	case ok := <-done:
		if ok {
			t.Fatal("leader 失败时 follower 不应拿到成功结果")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower 被残留 flight 卡住了（done 永不关闭）")
	}
	if got := pendingFlights(app); got != 0 {
		t.Fatalf("flight 表应已清空，仍有 %d 条", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("follower 不应自己再生成一次，generate 被调用 %d 次", calls)
	}
}

// TestThumbnailMalformedImageFallsBackAndRetries 覆盖 P1-3 (a) + 回退分支：
// 畸形图片只让这次生成失败（调用方发原图），不会 panic 进程、也不会锁死该 key。
func TestThumbnailMalformedImageFallsBackAndRetries(t *testing.T) {
	app := newThumbTestApp() // thumbGenerate 为空 ⇒ 走真实的 imageutil.Thumbnail

	dir := t.TempDir()
	path := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n truncated garbage that is not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := thumbTestContext(t, "/Items/3/Images/Primary?maxWidth=50")
	if data, ok := app.thumbnail(c, path, "tag-broken"); ok || data != nil {
		t.Fatalf("畸形图片应回退原图（ok=false），得到 ok=%v len=%d", ok, len(data))
	}
	if got := pendingFlights(app); got != 0 {
		t.Fatalf("畸形图片失败后不应残留 flight，仍有 %d 条", got)
	}

	// 换成合法 JPEG，沿用同一个 tag/key：能重新生成才说明没被残留状态锁死。
	jpegImage := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := range 100 {
		for x := range 200 {
			jpegImage.SetRGBA(x, y, color.RGBA{R: 200, G: uint8(x), B: uint8(y), A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(file, jpegImage, &jpeg.Options{Quality: 90}); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	c2, _ := thumbTestContext(t, "/Items/3/Images/Primary?maxWidth=50")
	data, ok := app.thumbnail(c2, path, "tag-broken")
	if !ok || len(data) == 0 {
		t.Fatalf("修好图片后同 key 应能生成缩略图，得到 ok=%v len=%d", ok, len(data))
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("缩略图无法解码: %v", err)
	}
	if got := decoded.Bounds(); got.Dx() != 50 || got.Dy() != 25 {
		t.Fatalf("缩略图尺寸 = %v，期望 50x25", got)
	}
	if got := pendingFlights(app); got != 0 {
		t.Fatalf("flight 表应已清空，仍有 %d 条", got)
	}
}

// TestThumbnailConcurrentSameKeyGeneratesOnce 是既有 HTTP 并发用例的单元级补充：
// 同 key N 个并发请求只生成一次，其余等待并拿到同一份字节。
func TestThumbnailConcurrentSameKeyGeneratesOnce(t *testing.T) {
	app := newThumbTestApp()
	var mu sync.Mutex
	generated := 0
	app.thumbGenerate = func(string, int, int, int) ([]byte, error) {
		mu.Lock()
		generated++
		mu.Unlock()
		// 放大生成窗口，让后到的请求有机会撞上同一个 flight。
		time.Sleep(50 * time.Millisecond)
		return []byte("webp-bytes"), nil
	}

	const requests = 8
	var wg sync.WaitGroup
	failures := make(chan string, requests)
	for range requests {
		wg.Go(func() {
			c, _ := thumbTestContext(t, "/Items/4/Images/Primary?maxWidth=90")
			data, ok := app.thumbnail(c, "/library/poster.jpg", "tag-concurrent")
			if !ok || !bytes.Equal(data, []byte("webp-bytes")) {
				failures <- "请求未拿到生成结果"
			}
		})
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	mu.Lock()
	defer mu.Unlock()
	if generated != 1 {
		t.Fatalf("%d 个同 key 并发请求只应生成 1 次，实际 %d 次", requests, generated)
	}
}
