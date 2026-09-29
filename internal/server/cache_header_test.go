package server

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCachePolicyHeaders 锁定各链路的缓存策略，防回归：
// 图片保 public+ETag；其余 API JSON 与外壳 HTML 一律 no-store；
// 前端静态资源 no-cache+ETag 且命中即 304；视频流（含扩展名变体）no-store。
//
// 这些头曾经只由 nginx 的 add_header 兜底，与 Go 自己发的头冲突（同一响应
// 两个 Cache-Control），且 nginx 的静态正则还误伤 /Videos/:id/stream.mp4。
// 现在策略全部落在 Go 侧，nginx 不再参与动态内容的缓存协商。
func TestCachePolicyHeaders(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	writeFile(t, filepath.Join(root, "ABF-018.nfo"), "<movie><title>ABF-018 标题</title><num>ABF-018</num></movie>\n")
	writeJPEGImage(t, filepath.Join(root, "poster.jpg"), 40, 20)

	app, ts, token := newProbeTestApp(t, root)

	// 不跟随重定向：stream 的 302 要看的是它自己的响应头。
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	get := func(path, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	// —— 动态 API：一律 no-store（含裸前缀、/emby 前缀与管理端）——
	cases := []struct{ path, token string }{
		{"/System/Info/Public", ""},
		{"/emby/System/Info/Public", ""},
		{"/Users/Me", token},
		{"/api/admin/status", token},
		{"/", ""},
		{"/admin", ""},
	}
	for _, item := range cases {
		if cc := get(item.path, item.token).Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s 的 Cache-Control = %q，期望 no-store", item.path, cc)
		}
	}

	id, err := app.db.MovieIDByPath(filepath.Join(root, "ABF-018.strm"))
	if err != nil || id == 0 {
		t.Fatalf("取影片 id 失败: %d %v", id, err)
	}
	itemID := strconv.FormatInt(id, 10)

	// —— 图片：保留 public 缓存，不能被 no-store 打掉 ——
	image := get("/Items/"+itemID+"/Images/Primary", "")
	if image.StatusCode != http.StatusOK {
		t.Fatalf("取图: %d", image.StatusCode)
	}
	if cc := image.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=") || strings.Contains(cc, "no-store") {
		t.Errorf("图片 Cache-Control = %q，应保留 public max-age 且不含 no-store", cc)
	}
	if image.Header.Get("ETag") == "" {
		t.Error("图片响应应带 ETag")
	}
	// 图片元信息是 JSON，仍归 no-store
	if cc := get("/Items/"+itemID+"/Images", token).Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("/Items/{id}/Images 元信息 Cache-Control = %q，期望 no-store", cc)
	}

	// —— 前端静态资源：ETag + no-cache，命中即 304（文件名无 hash，不能 immutable）——
	asset := get("/web/app.js", "")
	if cc := asset.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("/web/app.js Cache-Control = %q，期望 no-cache", cc)
	}
	etag := asset.Header.Get("ETag")
	if etag == "" {
		t.Fatal("/web/app.js 应带 ETag")
	}
	req, _ := http.NewRequest("GET", ts.URL+"/web/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	notModified, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	notModified.Body.Close()
	if notModified.StatusCode != http.StatusNotModified {
		t.Errorf("静态资源命中 ETag 应 304，实际 %d", notModified.StatusCode)
	}

	// 图标：一天缓存 + ETag
	favicon := get("/favicon.ico", "")
	if cc := favicon.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=86400") || favicon.Header.Get("ETag") == "" {
		t.Errorf("favicon 应带 max-age=86400 与 ETag，实际 %q", cc)
	}

	// —— 视频流 302：不许缓存，否则换源后客户端仍拉旧地址 ——
	for _, path := range []string{"/Videos/" + itemID + "/stream", "/Videos/" + itemID + "/stream.mp4"} {
		resp := get(path, token)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s 应 302，实际 %d", path, resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control = %q，期望 no-store", path, cc)
		}
	}
}
