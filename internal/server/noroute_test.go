package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// TestIsScannerProbe 扫描器判据只认「Emby 兼容客户端绝不会发」的形状，
// 宁可漏拦也不能误伤真实端点——误伤会让探针丢掉要补的接口。
func TestIsScannerProbe(t *testing.T) {
	scanner := []string{
		"/.svn/entries",
		"/old/.svn/entries",
		"/web/.svn/entries",
		"/.git-credentials.bak",
		"/.git/config",
		"/.env",
		"/emby-go.db",
		"/emby-115-cf.7z",
		"/emby-115-cf.hollc.top.tar.gz",
		"/secrets.yml",
		"/web.config",
		"/config/application.yml",
		"/database.yaml",
		"/wp-login.php",
		"/wp-admin/setup-config.php",
		"/phpmyadmin/index.php",
		"/xmlrpc.php",
		"/cgi-bin/luci",
	}
	for _, path := range scanner {
		if !isScannerProbe(path) {
			t.Errorf("isScannerProbe(%q) = false，期望 true（扫描器路径）", path)
		}
	}

	// Emby 兼容端点一律是无扩展名的驼峰段，必须全部放行。
	emby := []string{
		"/Users/1/Views",
		"/Users/Public",
		"/Branding/Configuration",
		"/Branding/Css",
		"/Persons",
		"/Items/Filters",
		"/Items/130151/Images/Primary",
		"/Sessions",
		"/Sessions/Playing",
		"/Localization/Options",
		"/Plugins",
		"/ScheduledTasks",
		"/Activity/Log",
		"/System/Info/Public",
		"/emby/Users/1/Views",
	}
	for _, path := range emby {
		if isScannerProbe(path) {
			t.Errorf("isScannerProbe(%q) = true，期望 false（Emby 端点）", path)
		}
	}
}

// Public scanner traffic is excluded. Unimplemented endpoints now retain only
// fixed resource families, never raw paths that might themselves be secrets.
func TestNoRouteSkipsScannerProbes(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "test.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	get404 := func(path string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d，期望 404", path, resp.StatusCode)
		}
	}

	scanner := []string{"/.svn/entries", "/emby-115-cf.7z", "/wp-login.php"}
	emby := []string{"/Branding/Configuration", "/Persons"}
	for _, path := range scanner {
		get404(path)
	}
	if probes, err := a.db.Probes(100); err != nil || len(probes) != 0 {
		t.Fatalf("scanner traffic was recorded: %v err=%v", probes, err)
	}
	for _, path := range emby {
		get404(path)
	}

	probes, err := a.db.Probes(100)
	if err != nil {
		t.Fatal(err)
	}
	recorded := make(map[string]bool, len(probes))
	for _, p := range probes {
		if path, ok := p["path"].(string); ok {
			recorded[path] = true
		}
	}
	for _, path := range scanner {
		if recorded[path] {
			t.Errorf("扫描器路径 %s 不应写入 api_probe", path)
		}
	}
	if len(probes) != 2 || !recorded["/[UNMATCHED]"] || !recorded["/persons/[UNMATCHED]"] {
		t.Fatalf("expected safe resource families, got %v", probes)
	}
	for _, path := range emby {
		if recorded[path] {
			t.Errorf("raw endpoint persisted: %s", path)
		}
	}
}
