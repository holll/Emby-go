package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// writeJPEGImage 写一张纯色 jpeg，用于验证图片接口的响应头与按需缩放。
func writeJPEGImage(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 50, B: 10, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, nil); err != nil {
		t.Fatal(err)
	}
}

// TestImageHeadersThumbnailAnd304 覆盖图片接口的三件事：
// 原图响应带 ETag/Cache-Control、If-None-Match 命中直接 304、maxWidth 请求按需缩放成 webp。
func TestImageHeadersThumbnailAnd304(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	writeFile(t, filepath.Join(root, "ABF-018.nfo"), "<movie><title>ABF-018 标题</title><num>ABF-018</num></movie>\n")
	posterPath := filepath.Join(root, "poster.jpg")
	writeJPEGImage(t, posterPath, 200, 100)

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()

	call := func(method, path, token, body string) (*http.Response, []byte) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, reader)
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
		return resp, raw
	}
	if resp, _ := call("POST", "/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d", resp.StatusCode)
	}
	_, authRaw := call("POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`)
	var auth struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil || auth.AccessToken == "" {
		t.Fatalf("登录失败: %v %s", err, authRaw)
	}
	libBody := `{"Name":"AV","Path":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	if resp, _ := call("POST", "/api/admin/libraries", auth.AccessToken, libBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("add library: %d", resp.StatusCode)
	}
	if resp, raw := call("POST", "/api/admin/scan", auth.AccessToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("scan: %d %s", resp.StatusCode, raw)
	}
	id, err := app.db.MovieIDByPath(filepath.Join(root, "ABF-018.strm"))
	if err != nil || id == 0 {
		t.Fatalf("取影片 id 失败: %d %v", id, err)
	}
	imagePath := "/Items/" + strconv.FormatInt(id, 10) + "/Images/Primary"

	// —— 原图：内容与磁盘一致，带 ETag 与 Cache-Control ——
	resp, body := call("GET", imagePath, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取原图: %d", resp.StatusCode)
	}
	raw, err := os.ReadFile(posterPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, raw) {
		t.Errorf("原图字节与磁盘文件不一致: %d vs %d", len(body), len(raw))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("缺少合规 ETag: %q", etag)
	}
	if cacheControl := resp.Header.Get("Cache-Control"); !strings.Contains(cacheControl, "max-age=") {
		t.Errorf("缺少 Cache-Control max-age: %q", cacheControl)
	}

	// —— 条件请求：命中 ETag 直接 304，且不再回图 ——
	req, _ := http.NewRequest("GET", ts.URL+imagePath, nil)
	req.Header.Set("If-None-Match", etag)
	notModified, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	notModified.Body.Close()
	if notModified.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match 命中应 304，实际 %d", notModified.StatusCode)
	}
	if notModified.Header.Get("ETag") != etag {
		t.Errorf("304 也应带 ETag: %q", notModified.Header.Get("ETag"))
	}

	// —— 缩略图：maxWidth=50 时应返回按比例缩小的 webp ——
	resp, body = call("GET", imagePath+"?maxWidth=50", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取缩略图: %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "image/webp" {
		t.Fatalf("缩略图 Content-Type = %q，期望 image/webp", contentType)
	}
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("缩略图无法解码: %v", err)
	}
	if decoded.Bounds().Dx() != 50 || decoded.Bounds().Dy() != 25 {
		t.Errorf("缩略图尺寸 = %v，期望 50x25", decoded.Bounds())
	}
	if len(body) >= len(raw) {
		t.Errorf("缩略图应小于原图: %d vs %d", len(body), len(raw))
	}

	// —— 不存在的 id 仍是 404 ——
	if resp, _ := call("GET", "/Items/999999/Images/Primary", "", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("不存在影片应 404，实际 %d", resp.StatusCode)
	}
}
