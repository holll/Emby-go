package server

import (
	"bytes"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// countVariantFiles 数缓存目录里有多少个变体文件（用来断言"只生成了一份"）。
func countVariantFiles(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".webp") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历缓存目录: %v", err)
	}
	return count
}

// TestImageDiskCacheReusesVariants 覆盖缩略图磁盘缓存的三条不变量：
// 同一张图的同一尺寸只生成一次（重启/内存淘汰后仍能复用）、
// 请求尺寸按档位向下对齐（195 与 190 共用 180 档）、源图变化后换新变体。
func TestImageDiskCacheReusesVariants(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	writeFile(t, filepath.Join(root, "ABF-018.nfo"), "<movie><title>ABF-018 标题</title></movie>\n")
	posterPath := filepath.Join(root, "poster.jpg")
	writeJPEGImage(t, posterPath, 200, 100)

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if !app.imgDisk.Enabled() {
		t.Fatal("默认配置应启用缩略图磁盘缓存")
	}
	if want := filepath.Join(root, "image-cache"); app.imgDisk.Dir() != want {
		t.Errorf("缓存目录 = %q，期望与 db 同级的 %q", app.imgDisk.Dir(), want)
	}
	cacheDir := app.imgDisk.Dir()

	// 统计真实生成次数：缓存全部命中时这个计数不应该增长。
	realGenerate := app.thumbGenerate
	var genMu sync.Mutex
	generated := 0
	app.thumbGenerate = func(path string, width, height, quality int) ([]byte, error) {
		genMu.Lock()
		generated++
		genMu.Unlock()
		// 放大生成窗口，让并发请求有机会撞上同一个 flight。
		time.Sleep(30 * time.Millisecond)
		return realGenerate(path, width, height, quality)
	}
	genCount := func() int {
		genMu.Lock()
		defer genMu.Unlock()
		return generated
	}

	ts := httptest.NewServer(app.Handler())
	defer ts.Close()

	call := func(path, token string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}
	post := func(path, token, body string) []byte {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, raw)
		}
		return raw
	}
	post("/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`)
	var auth struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(post("/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`), &auth); err != nil || auth.AccessToken == "" {
		t.Fatalf("登录失败: %v", err)
	}
	post("/api/admin/libraries", auth.AccessToken, `{"Name":"AV","Path":"`+strings.ReplaceAll(root, `\`, `\\`)+`"}`)
	post("/api/admin/scan", auth.AccessToken, "")
	id, err := app.db.MovieIDByPath(filepath.Join(root, "ABF-018.strm"))
	if err != nil || id == 0 {
		t.Fatalf("取影片 id 失败: %d %v", id, err)
	}
	imagePath := "/Items/" + strconv.FormatInt(id, 10) + "/Images/Primary"

	// —— 首次请求：生成一份变体并落盘 ——
	resp, body := call(imagePath+"?maxWidth=50", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取缩略图: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/webp" {
		t.Fatalf("Content-Type = %q，期望 image/webp", got)
	}
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("缩略图无法解码: %v", err)
	}
	if decoded.Bounds().Dx() != 50 || decoded.Bounds().Dy() != 25 {
		t.Errorf("缩略图尺寸 = %v，期望 50x25", decoded.Bounds())
	}
	if genCount() != 1 || countVariantFiles(t, cacheDir) != 1 {
		t.Fatalf("首次请求应生成 1 份变体: generated=%d files=%d", genCount(), countVariantFiles(t, cacheDir))
	}

	// —— 清空内存缓存：同一请求应命中磁盘，不再解码 ——
	app.imgThumb.Clear()
	resp, body2 := call(imagePath+"?maxWidth=50", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("二次取缩略图: %d", resp.StatusCode)
	}
	if !bytes.Equal(body, body2) {
		t.Error("磁盘命中的字节应与首次生成一致")
	}
	if genCount() != 1 {
		t.Errorf("磁盘命中不应再生成: generated=%d", genCount())
	}
	if got := countVariantFiles(t, cacheDir); got != 1 {
		t.Errorf("重复请求不应新增变体: files=%d", got)
	}

	// —— 尺寸档位：195 与 190 都向下对齐到 180，共用同一份变体 ——
	_, body195 := call(imagePath+"?maxWidth=195", "")
	decoded, _, err = image.Decode(bytes.NewReader(body195))
	if err != nil {
		t.Fatalf("195 缩略图无法解码: %v", err)
	}
	if decoded.Bounds().Dx() != 180 || decoded.Bounds().Dy() != 90 {
		t.Errorf("maxWidth=195 应落在 180 档，实际 %v", decoded.Bounds())
	}
	if got := countVariantFiles(t, cacheDir); got != 2 {
		t.Fatalf("180 档应新增 1 份变体: files=%d", got)
	}
	_, body190 := call(imagePath+"?maxWidth=190", "")
	decoded, _, err = image.Decode(bytes.NewReader(body190))
	if err != nil {
		t.Fatalf("190 缩略图无法解码: %v", err)
	}
	if decoded.Bounds().Dx() != 180 {
		t.Errorf("maxWidth=190 也应落在 180 档，实际 %v", decoded.Bounds())
	}
	if !bytes.Equal(body195, body190) {
		t.Error("同一档位应返回完全相同的字节")
	}
	if got := countVariantFiles(t, cacheDir); got != 2 {
		t.Errorf("同档位不应新增变体: files=%d", got)
	}

	// —— 并发去重：8 个同 URL 请求只应触发一次生成 ——
	app.imgThumb.Clear()
	base := genCount()
	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(ts.URL + imagePath + "?maxWidth=120")
			if err != nil {
				failures <- err.Error()
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusOK {
				failures <- "status " + strconv.Itoa(resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Errorf("并发请求失败: %s", failure)
	}
	if got := genCount() - base; got != 1 {
		t.Errorf("8 个并发同 URL 请求应只生成 1 次，实际 %d 次", got)
	}
	if got := countVariantFiles(t, cacheDir); got != 3 {
		t.Errorf("120 档应新增 1 份变体: files=%d", got)
	}

	// —— 源图变化：tag 变了就是一份新变体（旧变体留给清理） ——
	writeJPEGImage(t, posterPath, 200, 100)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(posterPath, future, future); err != nil {
		t.Fatal(err)
	}
	app.invalidateImageTag(posterPath)
	before := countVariantFiles(t, cacheDir)
	if resp, _ := call(imagePath+"?maxWidth=50", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("源图变化后取缩略图: %d", resp.StatusCode)
	}
	if got := countVariantFiles(t, cacheDir); got != before+1 {
		t.Errorf("源图变化应生成新变体: before=%d after=%d", before, got)
	}

	// —— 缓存策略不变：有 max-age，且不声明 immutable ——
	resp, _ = call(imagePath+"?maxWidth=50", "")
	cacheControl := resp.Header.Get("Cache-Control")
	if !strings.Contains(cacheControl, "max-age=") {
		t.Errorf("缺少 max-age: %q", cacheControl)
	}
	if strings.Contains(cacheControl, "immutable") {
		t.Errorf("不应声明 immutable: %q", cacheControl)
	}
}
