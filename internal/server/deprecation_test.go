package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestDeprecatedEndpointsAreRegistered 保证登记表与真实路由一致：
// 没有登记的路径或替代路径漂移都会让弃用承诺失效。
func TestDeprecatedEndpointsAreRegistered(t *testing.T) {
	if len(deprecatedEndpoints) == 0 {
		t.Fatal("v2 至少登记一个弃用端点")
	}
	registered := map[string]bool{}
	for _, r := range contractTestApp(t).router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, d := range deprecatedEndpoints {
		if !registered[d.Method+" "+d.Path] {
			t.Errorf("登记了不存在的端点：%s %s", d.Method, d.Path)
		}
		if d.Successor == "" || d.Since == "" || d.Sunset.IsZero() {
			t.Errorf("登记项缺少 successor/since/sunset：%+v", d)
		}
		if !registered[http.MethodPost+" "+d.Successor] {
			t.Errorf("替代路径不存在：POST %s", d.Successor)
		}
	}
}

// TestLegacyScanAdvertisesDeprecation 验证旧同步扫描仍可用且带 RFC 8594/8288 弃用头。
func TestLegacyScanAdvertisesDeprecation(t *testing.T) {
	_, ts, token := asyncScanFixture(t)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/scan", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Emby-Token", token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("旧同步扫描应继续可用，得到 %d %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation=%q", got)
	}
	if got, want := resp.Header.Get("Sunset"), legacySyncScan.Sunset.Format(http.TimeFormat); got != want {
		t.Errorf("Sunset=%q want %q", got, want)
	}
	if got := resp.Header.Get("Link"); !strings.Contains(got, `rel="successor-version"`) || !strings.Contains(got, legacySyncScan.Successor) {
		t.Errorf("Link=%q", got)
	}
	if got := resp.Header.Get("Warning"); !strings.Contains(got, "deprecated endpoint") {
		t.Errorf("Warning=%q", got)
	}
}
