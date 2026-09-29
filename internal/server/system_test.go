package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// TestInfoSchemeBehindProxy 服务在 nginx 之后由 nginx 终止 TLS，
// 此时必须采信 X-Forwarded-Proto：否则会把 https 站点通告成 http，
// 客户端拿到 http 的 LocalAddress/WanAddress 与 SupportsHttps=false 后可能连接回退。
func TestInfoSchemeBehindProxy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	writeFile(t, filepath.Join(root, "ABF-018.nfo"), "<movie><title>ABF-018 标题</title><num>ABF-018</num></movie>\n")
	_, ts, token := newProbeTestApp(t, root)

	infoWith := func(forwardedProto string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+"/System/Info", nil)
		req.Header.Set("X-Emby-Token", token)
		req.Host = "emby-115.hollc.top"
		if forwardedProto != "" {
			req.Header.Set("X-Forwarded-Proto", forwardedProto)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("解析 /System/Info: %v", err)
		}
		return v
	}

	behindTLS := infoWith("https")
	if behindTLS["SupportsHttps"] != true {
		t.Errorf("X-Forwarded-Proto=https 时 SupportsHttps 应为 true，实际 %v", behindTLS["SupportsHttps"])
	}
	if want := "https://emby-115.hollc.top"; behindTLS["LocalAddress"] != want {
		t.Errorf("LocalAddress = %v，期望 %s", behindTLS["LocalAddress"], want)
	}

	// 多个代理链时取最外层；非法值回退为直连判定（测试里是明文 http）
	if chained := infoWith("https, http"); chained["LocalAddress"] != "https://emby-115.hollc.top" {
		t.Errorf("多级 X-Forwarded-Proto 应取第一个: %v", chained["LocalAddress"])
	}
	direct := infoWith("")
	if direct["SupportsHttps"] != false || direct["LocalAddress"] != "http://emby-115.hollc.top" {
		t.Errorf("无代理头时应退回明文判定: %v %v", direct["SupportsHttps"], direct["LocalAddress"])
	}
}
