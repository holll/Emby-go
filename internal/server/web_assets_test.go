package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// webShellAssetRef 匹配页面外壳里引用的本地静态资源地址。
var webShellAssetRef = regexp.MustCompile(`/web/[A-Za-z0-9._/-]+`)

// TestWebShellAssetsAreServed 校验外壳引用的每个静态资源都真的能被服务端返回。
//
// 前端资源既要在 `//go:embed` 列表里，也要在路由表里，两处任缺一处，浏览器拿到的
// 就是 404（页面照样渲染，只是对应模块整体失效），node 侧测试读的是磁盘文件，
// 覆盖不到这条链路。
func TestWebShellAssetsAreServed(t *testing.T) {
	_, ts, _ := contractEnv(t)
	for _, page := range []string{"/", "/admin"} {
		resp, body := embyRaw(t, ts, http.MethodGet, page, "", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", page, resp.StatusCode)
		}
		refs := webShellAssetRef.FindAllString(string(body), -1)
		if len(refs) == 0 {
			t.Fatalf("GET %s 没有引用任何 /web 资源", page)
		}
		for _, ref := range refs {
			asset, assetBody := embyRaw(t, ts, http.MethodGet, ref, "", "")
			if asset.StatusCode != http.StatusOK {
				t.Errorf("GET %s = %d（外壳 %s 引用但服务端没有这个资源）", ref, asset.StatusCode, page)
				continue
			}
			if len(assetBody) == 0 {
				t.Errorf("GET %s 返回空正文", ref)
			}
			contentType := asset.Header.Get("Content-Type")
			switch {
			case strings.HasSuffix(ref, ".js"):
				if !strings.Contains(contentType, "javascript") {
					t.Errorf("GET %s Content-Type = %q，期望 javascript", ref, contentType)
				}
			case strings.HasSuffix(ref, ".css"):
				if !strings.Contains(contentType, "text/css") {
					t.Errorf("GET %s Content-Type = %q，期望 text/css", ref, contentType)
				}
			}
		}
	}
}
