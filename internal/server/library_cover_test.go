package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/store"
)

// TestLibraryCoverSettingsRoundTrip 媒体库封面的自动开关写入后能读回，
// 与合集开关互不影响（夹具初始把两个开关都关掉）。
func TestLibraryCoverSettingsRoundTrip(t *testing.T) {
	_, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})

	var got struct {
		Auto bool `json:"auto"`
	}
	status, raw := doJSON(t, ts, "GET", "/api/admin/library-cover/settings", token, "")
	if status != http.StatusOK {
		t.Fatalf("get settings: %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Auto {
		t.Fatalf("夹具初始应为关闭: %v %s", err, raw)
	}
	// 两个开关各存各的键：先都打开，再只关掉媒体库，合集应保持开启。
	for _, path := range []string{"/api/admin/collection-cover/settings", "/api/admin/library-cover/settings"} {
		if status, raw := doJSON(t, ts, "PUT", path, token, `{"auto":true}`); status != http.StatusOK {
			t.Fatalf("put %s: %d %s", path, status, raw)
		}
	}
	if status, raw := doJSON(t, ts, "PUT", "/api/admin/library-cover/settings", token, `{"auto":false}`); status != http.StatusOK {
		t.Fatalf("put settings: %d %s", status, raw)
	}
	_, raw = doJSON(t, ts, "GET", "/api/admin/library-cover/settings", token, "")
	if err := json.Unmarshal(raw, &got); err != nil || got.Auto {
		t.Fatalf("写入后应读回关闭: %v %s", err, raw)
	}
	if status, raw := doJSON(t, ts, "GET", "/api/admin/collection-cover/settings", token, ""); status != http.StatusOK {
		t.Fatalf("get collection settings: %d %s", status, raw)
	} else if err := json.Unmarshal(raw, &got); err != nil || !got.Auto {
		t.Fatalf("合集开关不应被媒体库开关影响: %v %s", err, raw)
	}

	for _, body := range []string{`{}`, `{"auto":"yes"}`, `{"autoo":true}`} {
		if status, raw := doJSON(t, ts, "PUT", "/api/admin/library-cover/settings", token, body); status != http.StatusBadRequest {
			t.Errorf("body %s 应返回 400，得到 %d %s", body, status, raw)
		}
	}
}

// TestLibraryCoverGeneratedAndServed 生成后媒体库封面换成本地 960×540 拼贴，
// 并通过 CollectionFolder DTO 暴露成 Primary 图。
func TestLibraryCoverGeneratedAndServed(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true, "c": true})
	library := onlyLibrary(t, app)
	before := app.libraryCoverPath(library)
	if before == "" || !strings.Contains(before, library.Path) {
		t.Fatalf("生成前应回落到库内代表图，得到 %q", before)
	}

	if status, raw := doJSON(t, ts, "POST", "/api/admin/library-cover/run", token, `{"all":true,"apply":true}`); status != http.StatusAccepted {
		t.Fatalf("run: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)

	covers, err := app.db.AppliedLibraryCovers()
	if err != nil {
		t.Fatal(err)
	}
	cover, ok := covers[library.ID]
	if !ok {
		t.Fatalf("应写入媒体库生效记录，实际 %v", covers)
	}
	if cover.Width != libraryCoverWidth || cover.Height != libraryCoverHeight {
		t.Fatalf("尺寸应为 %dx%d，得到 %dx%d", libraryCoverWidth, libraryCoverHeight, cover.Width, cover.Height)
	}
	if cover.Algorithm != collectionCoverAlgorithm {
		t.Fatalf("算法标识应为 %s，得到 %s", collectionCoverAlgorithm, cover.Algorithm)
	}
	if info, err := os.Stat(cover.Path); err != nil || info.Size() == 0 {
		t.Fatalf("拼贴文件应存在且非空: %v", err)
	}

	after := app.libraryCoverPath(library)
	if after != cover.Path {
		t.Fatalf("生成后应优先用拼贴封面，得到 %q，期望 %q", after, cover.Path)
	}
	if ratio := coverRatio(after); math.Abs(ratio-16.0/9.0) > 0.01 {
		t.Fatalf("封面比例应约 16:9，得到 %.4f", ratio)
	}

	status, raw := doJSON(t, ts, "GET", "/Library/MediaFolders", token, "")
	if status != http.StatusOK {
		t.Fatalf("media folders: %d %s", status, raw)
	}
	var folders struct {
		Items []struct {
			ImageTags               map[string]string `json:"ImageTags"`
			PrimaryImageAspectRatio float64           `json:"PrimaryImageAspectRatio"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(raw, &folders); err != nil || len(folders.Items) == 0 {
		t.Fatalf("解析媒体库列表失败: %v %s", err, raw)
	}
	item := folders.Items[0]
	if item.ImageTags["Primary"] == "" {
		t.Fatalf("媒体库应暴露 Primary 图: %s", raw)
	}
	if math.Abs(item.PrimaryImageAspectRatio-16.0/9.0) > 0.01 {
		t.Fatalf("DTO 比例应约 16:9，得到 %.4f", item.PrimaryImageAspectRatio)
	}

	// 图片端点要真的吐出这张拼贴，而不只是 DTO 里挂了个 tag。
	status, body := doJSON(t, ts, "GET", "/Items/"+externalLibraryID(library.ID)+"/Images/Primary", token, "")
	if status != http.StatusOK || len(body) == 0 {
		t.Fatalf("取媒体库封面: %d len=%d", status, len(body))
	}
	if want, err := os.ReadFile(cover.Path); err != nil || !bytes.Equal(body, want) {
		t.Fatalf("图片端点应返回生成的拼贴文件: %v", err)
	}

	// 再跑一次不重写库：内容没变就是同一张图。
	before2 := cover.Path
	if status, raw := doJSON(t, ts, "POST", "/api/admin/library-cover/run", token, `{"all":true,"apply":true}`); status != http.StatusAccepted {
		t.Fatalf("rerun: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)
	if covers, err := app.db.AppliedLibraryCovers(); err != nil || covers[library.ID].Path != before2 {
		t.Fatalf("重复生成应复用同一文件: %v %v", err, covers)
	}
}

// TestLibraryCoverKeepsCustomCover 库根目录自带海报时属于用户自定义封面，
// 自动拼贴不能覆盖它。
func TestLibraryCoverKeepsCustomCover(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	library := onlyLibrary(t, app)
	writePNGImage(t, filepath.Join(library.Path, "poster.png"), 400, 600)

	if status, raw := doJSON(t, ts, "POST", "/api/admin/library-cover/run", token, `{"all":true,"apply":true}`); status != http.StatusAccepted {
		t.Fatalf("run: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)

	if covers, err := app.db.AppliedLibraryCovers(); err != nil || len(covers) != 0 {
		t.Fatalf("自定义封面存在时不应写入生效记录: %v %v", err, covers)
	}
	if got := app.libraryCoverPath(library); got != filepath.Join(library.Path, "poster.png") {
		t.Fatalf("应保留库根目录海报，得到 %q", got)
	}
}

// TestCollectionCoverFallsBackToLandscape 成员没有 Primary 海报但有 Thumb 横图时，
// 按 Emby 的 Primary → Thumb 回退参与拼贴，而不是整部跳过。
func TestCollectionCoverFallsBackToLandscape(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true})
	dir := filepath.Join(onlyLibrary(t, app).Path, "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "b.strm"), "http://media.test/b.mp4\n")
	writeFile(t, filepath.Join(dir, "b.nfo"), "<movie><title>b</title><set><name>系列A</name></set></movie>\n")
	writePNGImage(t, filepath.Join(dir, "landscape.png"), 640, 360)
	if status, raw := doJSON(t, ts, "POST", "/api/admin/scan", token, ""); status != http.StatusOK {
		t.Fatalf("rescan: %d %s", status, raw)
	}

	_, paths, err := app.collectionCoverSources("系列A")
	if err != nil {
		t.Fatal(err)
	}
	var landscape bool
	for _, path := range paths {
		if filepath.Base(path) == "landscape.png" {
			landscape = true
		}
	}
	if len(paths) != 2 || !landscape {
		t.Fatalf("应把只有 landscape 的成员也算进来，得到 %v", paths)
	}
}

// onlyLibrary 返回唯一一个媒体库，测试 fixture 只会建一个。
func onlyLibrary(t *testing.T, app *App) store.Library {
	t.Helper()
	libs, err := app.db.Libraries()
	if err != nil || len(libs) != 1 {
		t.Fatalf("fixture 应只有一个媒体库: %v %v", err, libs)
	}
	return libs[0]
}

// TestLibraryCoverAutoRunsAfterScan 开关打开时，扫描提交新影片后自动补媒体库封面。
func TestLibraryCoverAutoRunsAfterScan(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true})
	if status, raw := doJSON(t, ts, "PUT", "/api/admin/library-cover/settings", token, `{"auto":true}`); status != http.StatusOK {
		t.Fatalf("enable auto: %d %s", status, raw)
	}
	library := onlyLibrary(t, app)
	dir := filepath.Join(library.Path, "d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "d.strm"), "http://media.test/d.mp4\n")
	writeFile(t, filepath.Join(dir, "d.nfo"), "<movie><title>d</title></movie>\n")
	writePNGImage(t, filepath.Join(dir, "poster.png"), 300, 450)
	if status, raw := doJSON(t, ts, "POST", "/api/admin/scan", token, ""); status != http.StatusOK {
		t.Fatalf("scan: %d %s", status, raw)
	}
	waitCollectionCoverTask(t, app)

	covers, err := app.db.AppliedLibraryCovers()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := covers[library.ID]; !ok {
		t.Fatalf("自动触发应写入媒体库生效记录: %v", covers)
	}
}
