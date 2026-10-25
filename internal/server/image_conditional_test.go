package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// 这两个决定是「故意不做」的，容易被人顺手对齐 Jellyfin 改坏，所以用测试锁住：
//  1. Cache-Control 不加 immutable——图片没有内容寻址的永久 URL，文件可能被替换；
//  2. If-None-Match 支持多值、弱校验 W/ 与 *，而不只是单个精确 ETag。
func TestImageConditionalRequestsAndCachePolicy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "IMG-001.strm"), "http://media.test/img.mp4\n")
	writeFile(t, filepath.Join(root, "IMG-001.nfo"), "<movie><title>IMG</title></movie>\n")
	writeJPEGImage(t, filepath.Join(root, "poster.jpg"), 120, 60)

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()

	call := func(method, path, token, body string) []byte {
		t.Helper()
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
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
		return raw
	}
	call("POST", "/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`)
	var auth struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(call("POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`), &auth); err != nil || auth.AccessToken == "" {
		t.Fatalf("登录失败: %v", err)
	}
	call("POST", "/api/admin/libraries", auth.AccessToken, `{"Name":"AV","Path":"`+strings.ReplaceAll(root, `\`, `\\`)+`"}`)
	call("POST", "/api/admin/scan", auth.AccessToken, "")
	id, err := app.db.MovieIDByPath(filepath.Join(root, "IMG-001.strm"))
	if err != nil || id == 0 {
		t.Fatalf("取影片 id 失败: %d %v", id, err)
	}
	imagePath := "/Items/" + strconv.FormatInt(id, 10) + "/Images/Primary"

	get := func(path, ifNoneMatch string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}

	resp, raw := get(imagePath, "")
	if resp.StatusCode != http.StatusOK || len(raw) == 0 {
		t.Fatalf("原图应 200 且带内容: %d %d", resp.StatusCode, len(raw))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("缺少 ETag")
	}
	cacheControl := resp.Header.Get("Cache-Control")
	if strings.Contains(cacheControl, "immutable") {
		t.Errorf("原图的 Cache-Control 不应包含 immutable: %q", cacheControl)
	}
	if !strings.Contains(cacheControl, "max-age=") || !strings.Contains(cacheControl, "must-revalidate") {
		t.Errorf("原图的 Cache-Control 应带 max-age 与 must-revalidate: %q", cacheControl)
	}

	thumb, _ := get(imagePath+"?maxWidth=60", "")
	if thumb.StatusCode != http.StatusOK {
		t.Fatalf("缩略图应 200，实际 %d", thumb.StatusCode)
	}
	if cacheControl := thumb.Header.Get("Cache-Control"); strings.Contains(cacheControl, "immutable") {
		t.Errorf("缩略图的 Cache-Control 不应包含 immutable: %q", cacheControl)
	}

	for _, header := range []string{
		etag,                   // 单值
		`"stale-tag", ` + etag, // 多值里含命中值
		"W/" + etag,            // 弱校验
		"*",                    // 通配
	} {
		resp, body := get(imagePath, header)
		if resp.StatusCode != http.StatusNotModified {
			t.Errorf("If-None-Match=%q 应 304，实际 %d", header, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("304 不应回图: %d bytes", len(body))
		}
	}

	if resp, _ := get(imagePath, `"other-tag"`); resp.StatusCode != http.StatusOK {
		t.Errorf("不匹配的 If-None-Match 应回 200，实际 %d", resp.StatusCode)
	}
}
