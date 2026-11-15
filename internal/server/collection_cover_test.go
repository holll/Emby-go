package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/collage"
	"emby-go/internal/config"
	"emby-go/internal/store"

	"github.com/deepteams/webp"
)

// collectionCoverFixture 建一个真实可扫描的媒体库，并返回 app、服务地址与登录 token。
// posters 决定每部片是否带海报；每部片各占一个子目录，海报按扫描器认的 poster.png 命名。
func collectionCoverFixture(t *testing.T, posters map[string]bool) (*App, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	names := make([]string, 0, len(posters))
	for name := range posters {
		names = append(names, name)
	}
	slices.Sort(names)
	for i, name := range names {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, name+".strm"), "http://media.test/"+name+".mp4\n")
		writeFile(t, filepath.Join(dir, name+".nfo"),
			"<movie><title>"+name+"</title><set><name>系列A</name></set></movie>\n")
		if posters[name] {
			// 各片宽度不同，覆盖「按高度等比缩放 + 水平居中裁切」的不同分支。
			writePNGImage(t, filepath.Join(dir, "poster.png"), 300+40*i, 450)
		}
	}
	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)
	token := loginFixtureAdmin(t, ts)
	// 自动触发默认开启；fixture 统一关掉，让每个用例自己决定要不要自动生成，
	// 否则建库扫描就会排一个封面任务，后续手动 run 会撞任务槽拿 409。
	for _, path := range []string{"/api/admin/collection-cover/settings", "/api/admin/library-cover/settings"} {
		if status, raw := doJSON(t, ts, "PUT", path, token, `{"auto":false}`); status != http.StatusOK {
			t.Fatalf("disable auto %s: %d %s", path, status, raw)
		}
	}
	libBody := `{"Name":"AV","Path":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	if status, raw := doJSON(t, ts, "POST", "/api/admin/libraries", token, libBody); status != http.StatusOK {
		t.Fatalf("add library: %d %s", status, raw)
	}
	if status, raw := doJSON(t, ts, "POST", "/api/admin/scan", token, ""); status != http.StatusOK {
		t.Fatalf("scan: %d %s", status, raw)
	}
	return app, ts, token
}

func writePNGImage(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / width), G: uint8(y * 255 / height), B: 120, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

func loginFixtureAdmin(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	if status, _ := doJSON(t, ts, "POST", "/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`); status != http.StatusNoContent {
		t.Fatalf("initialize: %d", status)
	}
	_, raw := doJSON(t, ts, "POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`)
	var auth struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil || auth.AccessToken == "" {
		t.Fatalf("登录失败: %v %s", err, raw)
	}
	return auth.AccessToken
}

func doJSON(t *testing.T, ts *httptest.Server, method, path, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := readAll(resp)
	return resp.StatusCode, raw
}

func readAll(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

// TestCollectionCoverPreviewRendersEmbyGeometry 预览端点必须产出 640×360 的 WebP。
func TestCollectionCoverPreviewRendersEmbyGeometry(t *testing.T) {
	_, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true, "c": true})
	status, raw := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, "")
	if status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, raw)
	}
	img, err := webp.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("预览不是可解码的 webp：%v", err)
	}
	if got := img.Bounds().Size(); got.X != 640 || got.Y != 360 {
		t.Errorf("尺寸 = %dx%d，期望 640x360", got.X, got.Y)
	}
}

// TestCollectionCoverPreviewMatchesDefaultApply 默认预览与默认 apply 必须是同一张图：
// 预览只差「不写库」，不能因为 GET 没传参数就换成另一套渲染参数（reflection）。
func TestCollectionCoverPreviewMatchesDefaultApply(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true, "c": true})
	status, preview := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, "")
	if status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, preview)
	}
	if status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`); status != http.StatusAccepted {
		t.Fatalf("run apply: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)
	cover, ok, err := app.db.AppliedCover("系列A")
	if err != nil || !ok {
		t.Fatalf("生效记录未写入: ok=%v err=%v", ok, err)
	}
	applied, err := os.ReadFile(cover.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(preview, applied) {
		t.Errorf("默认预览与默认发布不是同一张图：preview=%d applied=%d", len(preview), len(applied))
	}
}

// TestCollectionCoverPreviewReusesCache 同一合集连续两次预览复用同一个文件。
func TestCollectionCoverPreviewReusesCache(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	first, _ := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, "")
	if first != http.StatusOK {
		t.Fatalf("first preview: %d", first)
	}
	entries := collectionCoverFiles(t, app)
	if len(entries) != 1 {
		t.Fatalf("首次预览应只有一个封面文件，实际 %d: %v", len(entries), entries)
	}
	if second, _ := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, ""); second != http.StatusOK {
		t.Fatalf("second preview: %d", second)
	}
	if again := collectionCoverFiles(t, app); len(again) != 1 {
		t.Errorf("缓存命中不该再落新文件，实际 %d: %v", len(again), again)
	}
}

// TestCollectionCoverInvalidatesOnPosterChange 海报文件被替换后必须重新生成。
func TestCollectionCoverInvalidatesOnPosterChange(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	if status, _ := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, ""); status != http.StatusOK {
		t.Fatalf("preview: %d", status)
	}
	before := collectionCoverFiles(t, app)
	movies, err := app.db.LatestVisible(0, nil, "系列A", 10, 0)
	if err != nil || len(movies) == 0 {
		t.Fatalf("取成员失败: %v", err)
	}
	poster := movies[0].PosterPath
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(poster, future, future); err != nil {
		t.Fatal(err)
	}
	// posterTag 是 5 分钟进程内缓存；扫描/刮削替换海报后会显式失效，
	// 这里照同一条路径失效，否则读到的仍是旧 mtime。
	app.invalidateImageTag(poster)
	if status, _ := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, ""); status != http.StatusOK {
		t.Fatalf("preview after touch: %d", status)
	}
	if after := collectionCoverFiles(t, app); len(after) <= len(before) {
		t.Errorf("海报 mtime 变化后应生成新文件：before=%v after=%v", before, after)
	}
}

// TestCollectionCoverApplyPublishesImage apply 后合集 Primary 图与 DTO 比例都换成拼贴。
func TestCollectionCoverApplyPublishesImage(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true, "c": true})
	status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`)
	if status != http.StatusAccepted {
		t.Fatalf("run apply: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)

	cover, ok, err := app.db.AppliedCover("系列A")
	if err != nil || !ok {
		t.Fatalf("生效记录未写入: ok=%v err=%v", ok, err)
	}
	if cover.Algorithm != collectionCoverAlgorithm || cover.Width != 640 || cover.Height != 360 {
		t.Errorf("生效记录字段不对: %+v", cover)
	}

	imagePath := "/Items/" + boxsetID("系列A") + "/Images/Primary"
	resp, err := http.Get(ts.URL + imagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	served, _ := readAll(resp)
	want, err := os.ReadFile(cover.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(served, want) {
		t.Errorf("Primary 图字节与生成的封面不一致：%d vs %d", len(served), len(want))
	}

	_, body := doJSON(t, ts, "GET", "/Items/"+boxsetID("系列A"), token, "")
	var item map[string]any
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("详情不是 JSON: %v %s", err, body)
	}
	tags, _ := item["ImageTags"].(map[string]any)
	if tags == nil || tags["Primary"] == "" {
		t.Fatalf("详情缺少 Primary tag: %v", item)
	}
	ratio, _ := item["PrimaryImageAspectRatio"].(float64)
	if ratio < 1.77 || ratio > 1.79 {
		t.Errorf("PrimaryImageAspectRatio = %v，期望 ≈1.7778", ratio)
	}
}

// TestCollectionCoverKeepsCustomCover 自定义封面默认不被覆盖，显式 overwrite 才替换。
func TestCollectionCoverKeepsCustomCover(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	custom := filepath.Join(t.TempDir(), "custom.webp")
	writePNGImage(t, custom, 400, 600)
	if err := app.db.SetAppliedCover(store.CollectionCover{
		Collection: "系列A", Path: custom, SourceHash: "custom", Algorithm: "custom-upload",
		Width: 400, Height: 600, AppliedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`)
	if status != http.StatusAccepted {
		t.Fatalf("run apply: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)
	kept, _, err := app.db.AppliedCover("系列A")
	if err != nil || kept.Algorithm != "custom-upload" {
		t.Errorf("自定义封面被覆盖了: %+v err=%v", kept, err)
	}

	status, raw = doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true,"overwrite":true}`)
	if status != http.StatusAccepted {
		t.Fatalf("run overwrite: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)
	replaced, _, err := app.db.AppliedCover("系列A")
	if err != nil || replaced.Algorithm != collectionCoverAlgorithm {
		t.Errorf("overwrite 未替换自定义封面: %+v err=%v", replaced, err)
	}
}

// TestCollectionCoverRevertRestoresRepresentative revert 后回退到代表海报。
func TestCollectionCoverRevertRestoresRepresentative(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`)
	if status != http.StatusAccepted {
		t.Fatalf("run apply: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)

	status, raw = doJSON(t, ts, "POST", "/api/admin/collection-cover/revert", token, `{"collection":"系列A"}`)
	if status != http.StatusOK {
		t.Fatalf("revert: %d %s", status, raw)
	}
	if _, ok, err := app.db.AppliedCover("系列A"); err != nil || ok {
		t.Errorf("revert 后仍有生效记录: ok=%v err=%v", ok, err)
	}
	_, body := doJSON(t, ts, "GET", "/Items/"+boxsetID("系列A"), token, "")
	var item map[string]any
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("详情不是 JSON: %v", err)
	}
	if tags, _ := item["ImageTags"].(map[string]any); tags == nil || tags["Primary"] == "" {
		t.Errorf("回退后应仍有代表海报: %v", item)
	}
}

// TestCollectionCoverRevertAllInvalidatesAppliedCache revert 之后进程内「已生效封面」
// 缓存必须立刻失效：它带着 30 秒 TTL，靠 TTL 兜底会让列表在退掉封面后继续按旧状态出图。
func TestCollectionCoverRevertAllInvalidatesAppliedCache(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	if status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`); status != http.StatusAccepted {
		t.Fatalf("run apply: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)
	if _, ok := app.appliedCovers()["系列A"]; !ok {
		t.Fatal("apply 后应进入已生效缓存")
	}
	status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/revert", token, `{"all":true}`)
	if status != http.StatusOK {
		t.Fatalf("revert all: %d %s", status, raw)
	}
	var resp struct {
		Reverted int `json:"reverted"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Reverted != 1 {
		t.Fatalf("应退掉一条封面: %v %s", err, raw)
	}
	if _, ok := app.appliedCovers()["系列A"]; ok {
		t.Error("revert 后已生效缓存应立即失效")
	}
}

// TestCollectionCoverSkipsMissingPosters 没有海报或海报损坏时跳过，不中断整批。
func TestCollectionCoverSkipsMissingPosters(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": false, "b": false})
	status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`)
	if status != http.StatusAccepted {
		t.Fatalf("run: %d %s", status, raw)
	}
	s := waitCollectionCoverTask(t, app)
	if s.Skipped != 1 || s.Failed != 0 {
		t.Errorf("无海报应跳过：%+v", s)
	}
	if len(s.SkipNotes) != 1 || s.SkipNotes[0].Name != "系列A" || s.SkipNotes[0].Reason != "no posters" {
		t.Errorf("应写明跳过原因：%+v", s.SkipNotes)
	}
	if _, ok, _ := app.db.AppliedCover("系列A"); ok {
		t.Error("跳过时不该写生效记录")
	}

	// 损坏的海报：解码失败同样只跳过。
	root := filepath.Dir(app.cfg.DBPath)
	writeFile(t, filepath.Join(root, "a.png"), "not an image")
	writeFile(t, filepath.Join(root, "b.png"), "not an image")
	if status, raw := doJSON(t, ts, "POST", "/api/admin/scan", token, ""); status != http.StatusOK {
		t.Fatalf("rescan: %d %s", status, raw)
	}
	if status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, `{"collections":["系列A"],"apply":true}`); status != http.StatusAccepted {
		t.Fatalf("run corrupt: %d %s", status, raw)
	}
	s = waitCollectionCoverTask(t, app)
	if s.Skipped != 1 || s.Failed != 0 {
		t.Errorf("损坏海报应跳过：%+v", s)
	}
}

// TestCollectionCoverLeavesSourcesUntouched 生成过程不得修改源海报文件。
func TestCollectionCoverLeavesSourcesUntouched(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	movies, err := app.db.LatestVisible(0, nil, "系列A", 10, 0)
	if err != nil || len(movies) == 0 {
		t.Fatalf("取成员失败: %v", err)
	}
	type snapshot struct {
		data []byte
		mod  time.Time
	}
	before := make(map[string]snapshot, len(movies))
	for _, m := range movies {
		data, err := os.ReadFile(m.PosterPath)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(m.PosterPath)
		if err != nil {
			t.Fatal(err)
		}
		before[m.PosterPath] = snapshot{data: data, mod: info.ModTime()}
	}
	if status, raw := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, ""); status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, raw)
	}
	for path, snap := range before {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("源海报被删了: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, snap.data) || !info.ModTime().Equal(snap.mod) {
			t.Errorf("源海报被修改: %s", path)
		}
	}
}

// TestCollectionCoverRequestValidation 入口参数只校验一次，非法值一律 400。
func TestCollectionCoverRequestValidation(t *testing.T) {
	_, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	cases := []struct{ name, body string }{
		{"未知字段", `{"collections":["系列A"],"bogus":1}`},
		{"collections 与 all 同给", `{"collections":["系列A"],"all":true}`},
		{"都不给", `{}`},
		{"空合集名", `{"collections":["  "]}`},
		{"quality 越界", `{"collections":["系列A"],"quality":101}`},
		{"quality 为零", `{"collections":["系列A"],"quality":0}`},
		{"width 太小", `{"collections":["系列A"],"width":63}`},
		{"height 太大", `{"collections":["系列A"],"height":4097}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if status, raw := doJSON(t, ts, "POST", "/api/admin/collection-cover/run", token, tc.body); status != http.StatusBadRequest {
				t.Errorf("status=%d 期望 400：%s", status, raw)
			}
		})
	}
	if status, _ := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection=", token, ""); status != http.StatusBadRequest {
		t.Errorf("空 collection 应 400，实际 %d", status)
	}
	if status, _ := doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("不存在的合集"), token, ""); status != http.StatusNotFound {
		t.Errorf("不存在的合集应 404，实际 %d", status)
	}
}

// TestCollectionCoverConcurrentPreview 并发预览同一合集结果一致且不重复渲染。
func TestCollectionCoverConcurrentPreview(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true, "c": true})
	const workers = 4
	var wg sync.WaitGroup
	results := make([][]byte, workers)
	statuses := make([]int, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], results[i] = doJSON(t, ts, "GET", "/api/admin/collection-cover/preview?collection="+urlQuery("系列A"), token, "")
		}()
	}
	wg.Wait()
	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("worker %d: %d", i, status)
		}
	}
	for i := 1; i < workers; i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Errorf("并发结果不一致：worker 0 与 %d", i)
		}
	}
	if files := collectionCoverFiles(t, app); len(files) != 1 {
		t.Errorf("并发只该产出一个文件，实际 %d: %v", len(files), files)
	}
}

// TestCoverVariantFollowersDoNotRender 同一 key 只允许 leader 渲染：先占住 leader 位置，
// 并发的调用必须等 leader 的结果，而不是各自重新解压、拼贴同一批海报。
// 「只产出一个文件」证不了这一点——同 key 的重复渲染写的是同一个路径。
func TestCoverVariantFollowersDoNotRender(t *testing.T) {
	app, _, _ := collectionCoverFixture(t, map[string]bool{"a": true})
	dir := app.collectionCoverDir()
	const key = "flight-only-test"
	dest := filepath.Join(dir, key+".webp")
	leader, ok := app.beginCoverFlight(dest)
	if !ok {
		t.Fatal("测试应拿到 leader 位置")
	}
	const workers = 4
	var started, wg sync.WaitGroup
	started.Add(workers)
	paths := make([]string, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started.Done()
			paths[i], errs[i] = app.coverVariant(context.Background(), dir, key, nil, collage.Layout{Width: 640, Height: 360}, 90, true)
		}()
	}
	// 等所有 goroutine 就绪再给一点时间登记成 follower，然后由 leader 发布结果。
	started.Wait()
	time.Sleep(200 * time.Millisecond)
	app.finishCoverFlight(dest, leader, dest, nil)
	wg.Wait()
	for i := range workers {
		if errs[i] != nil || paths[i] != dest {
			t.Fatalf("follower %d 应等到 leader 结果: path=%q err=%v", i, paths[i], errs[i])
		}
	}
	// follower 不落盘：这条路径上谁都没渲染。
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("follower 不应写盘: %v", err)
	}
}

func collectionCoverFiles(t *testing.T, a *App) []string {
	t.Helper()
	entries, err := os.ReadDir(a.collectionCoverDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// waitCollectionCoverTask 等到当前合集封面任务结束并返回终态。
func waitCollectionCoverTask(t *testing.T, a *App) coverTaskStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		a.coverTaskMu.RLock()
		s := a.coverStatus
		a.coverTaskMu.RUnlock()
		if !s.Running {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("合集封面任务超时未结束")
	return coverTaskStatus{}
}

func urlQuery(name string) string {
	return strings.ReplaceAll(fmt.Sprintf("%s", name), " ", "%20")
}
