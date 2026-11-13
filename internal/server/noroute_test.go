package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// NoRoute 只做「禁止缓存 + 404 信封」，不再记录未实现端点。
func TestNoRouteContract(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "test.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	for _, path := range []string{"/.svn/entries", "/emby-115-cf.7z", "/wp-login.php", "/Branding/Configuration", "/Artists"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(body), `"NOT_FOUND"`) {
			t.Fatalf("GET %s = %d cache=%q body=%s", path, resp.StatusCode, resp.Header.Get("Cache-Control"), body)
		}
	}
}
