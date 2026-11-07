package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"emby-go/internal/metatube"
)

// 同一影片连续 inspect 重排/替换列表：旧预览、新预览和无版本旧链接不能串图。
// 字节缓存、列表缓存分别失效时也不能把旧下标套在新列表上。
func TestScrapeStillVersionIsolation(t *testing.T) {
	app, ts, token := contractEnv(t)
	var mu sync.Mutex
	images := []string{"https://p/a.jpg", "https://p/b.jpg"}
	lookups, downloads, forcedLookups := 0, 0, 0
	lookupFailed, downloadFailed := false, false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v1/movies/") {
			lookups++
			if r.URL.Query().Get("lazy") == "false" {
				forcedLookups++
			}
			if lookupFailed {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"provider": "FANZA", "id": "abc00123", "preview_images": images,
			}})
			return
		}
		downloads++
		if downloadFailed {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		// 代理原样转发，无需在本回归中做 JPEG 解码；字节直接标识所选地址。
		_, _ = io.WriteString(w, r.URL.Query().Get("url"))
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	inspect := func() []scrapeStill {
		t.Helper()
		resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token, `{"provider":"FANZA","id":"abc00123","update":true}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("inspect: %d %s", resp.StatusCode, raw)
		}
		var out scrapeInspect
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Stills) != 2 {
			t.Fatalf("stills = %+v", out.Stills)
		}
		return out.Stills
	}
	fetch := func(target string, status int, want string) {
		t.Helper()
		resp, err := http.Get(ts.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status || (status == http.StatusOK && string(raw) != want) {
			t.Fatalf("%s: %d %q, want %d %q", target, resp.StatusCode, raw, status, want)
		}
		if status == http.StatusOK && !strings.Contains(resp.Header.Get("Cache-Control"), "max-age=300") {
			t.Fatal("成功图片未保留浏览器缓存契约")
		}
		if status != http.StatusOK && resp.Header.Get("Cache-Control") != "" {
			t.Fatal("失败响应不能被浏览器缓存")
		}
	}
	legacy := "/api/admin/scrape/image?kind=still&provider=FANZA&id=abc00123&index=0"
	fetch(legacy, http.StatusOK, "https://p/a.jpg") // 先污染旧列表和旧字节缓存
	first := inspect()
	firstURL, err := url.Parse(first[0].Preview)
	if err != nil {
		t.Fatal(err)
	}
	version := firstURL.Query().Get("version")
	if len(version) != 64 {
		t.Fatalf("missing list version: %s", first[0].Preview)
	}
	mu.Lock()
	images = []string{"https://p/b.jpg", "https://p/c.jpg"}
	mu.Unlock()
	second := inspect()
	if second[0].Preview == first[0].Preview {
		t.Fatal("列表变化却复用了同一浏览器 URL")
	}
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")
	fetch(first[0].Preview, http.StatusOK, "https://p/a.jpg")
	fetch(second[1].Preview, http.StatusOK, "https://p/c.jpg")
	fetch(first[1].Preview, http.StatusOK, "https://p/b.jpg")
	fetch(legacy, http.StatusOK, "https://p/a.jpg")
	// 两个已打开的预览并发请求同一下标，各自仍命中自己的版本。
	var requests sync.WaitGroup
	for range 8 {
		for _, candidate := range []struct{ target, want string }{
			{first[0].Preview, "https://p/a.jpg"},
			{second[0].Preview, "https://p/b.jpg"},
		} {
			requests.Go(func() {
				resp, err := http.Get(ts.URL + candidate.target)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusOK || string(raw) != candidate.want {
					t.Errorf("并发预览: status=%d body=%q err=%v want=%q", resp.StatusCode, raw, err, candidate.want)
				}
			})
		}
	}
	requests.Wait()
	mu.Lock()
	if lookups != 3 || downloads != 5 || forcedLookups != 2 {
		t.Fatalf("inspect 应强刷两次、图片不应重取列表: lookups=%d downloads=%d forced=%d", lookups, downloads, forcedLookups)
	}
	mu.Unlock()

	// 仅图片字节淘汰时，仍应按各自列表快照重下，不能查询最新列表后偷换下标。
	app.imgThumb.Clear()
	fetch(first[0].Preview, http.StatusOK, "https://p/a.jpg")
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")
	mu.Lock()
	if lookups != 3 || downloads != 7 {
		t.Fatalf("字节淘汰后应复用版本快照: lookups=%d downloads=%d", lookups, downloads)
	}
	mu.Unlock()

	// 两类缓存都失效，当前列表不再匹配旧版本：404，绝不能取新列表的同一下标。
	app.scrapeStills.Clear()
	app.imgThumb.Clear()
	fetch(first[0].Preview, http.StatusNotFound, "")
	mu.Lock()
	if lookups != 4 || downloads != 7 {
		t.Fatalf("失效旧版本不应下图: lookups=%d downloads=%d", lookups, downloads)
	}
	mu.Unlock()
	// 当前版本可以从上游同一列表重建，再次请求命中字节缓存。
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")
	mu.Lock()
	if lookups != 5 || downloads != 8 {
		t.Fatalf("同版本重建/缓存不符: lookups=%d downloads=%d", lookups, downloads)
	}
	mu.Unlock()
	// 仅列表快照丢失但同版本字节尚存，仍可安全返回原图，不必再次查询。
	app.scrapeStills.Clear()
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")

	// 列表及字节均丢失且上游不可用时保留 502，不伪装成过期 404，也不下图。
	app.imgThumb.Clear()
	mu.Lock()
	lookupFailed = true
	mu.Unlock()
	fetch(second[0].Preview, http.StatusBadGateway, "")
	mu.Lock()
	if lookups != 6 || downloads != 8 {
		t.Fatalf("上游列表失败不应下载图片: lookups=%d downloads=%d", lookups, downloads)
	}
	mu.Unlock()

	// 列表恢复但图片下载失败时仍返回 502，失败字节不缓存，也不自动重试。
	mu.Lock()
	lookupFailed, downloadFailed = false, true
	mu.Unlock()
	fetch(second[0].Preview, http.StatusBadGateway, "")
	mu.Lock()
	if lookups != 7 || downloads != 9 {
		t.Fatalf("下载失败应只重建一次列表并尝试一次图片: lookups=%d downloads=%d", lookups, downloads)
	}
	downloadFailed = false
	mu.Unlock()
	// 调用方再次请求，复用刚重建的列表；成功后才可缓存图片字节。
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")
	fetch(second[0].Preview, http.StatusOK, "https://p/b.jpg")
	mu.Lock()
	if lookups != 7 || downloads != 10 {
		t.Fatalf("下载恢复应复用列表且仅缓存成功字节: lookups=%d downloads=%d", lookups, downloads)
	}
	mu.Unlock()

	for _, target := range []string{
		legacy + "&version=invalid",
		legacy + "&version=" + strings.Repeat("G", 64),
		legacy + "&version=" + strings.ToUpper(version),
		strings.Replace(legacy, "kind=still", "kind=primary", 1) + "&version=" + version,
	} {
		fetch(target, http.StatusBadRequest, "")
	}
}

// 新旧链接同时遇到列表/字节冷缓存：旧版本必须 404，当前版本只能下载当前下标。
func TestScrapeStillVersionConcurrentColdCache(t *testing.T) {
	app, ts, _ := contractEnv(t)
	var mu sync.Mutex
	lookups, downloads := 0, 0
	images := []string{"https://p/b.jpg", "https://p/c.jpg"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v1/movies/") {
			lookups++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"provider": "FANZA", "id": "abc00123", "preview_images": images,
			}})
			return
		}
		downloads++
		_, _ = io.WriteString(w, r.URL.Query().Get("url"))
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	_, oldVersion := encodeStillList([]string{"https://p/a.jpg", "https://p/b.jpg"})
	_, currentVersion := encodeStillList(images)
	base := "/api/admin/scrape/image?kind=still&provider=FANZA&id=abc00123&index=0&version="
	// 两种缓存初始均为空。统一起跑，允许当前版本请求在运行中填充缓存，
	// 不要求 singleflight 或固定上游请求次数；只验证冷启动期间不串版本。
	// D-4/A 饱和改为 503；这里保持四路以内，拒绝行为由下载准入专项验证。
	start := make(chan struct{})
	var requests sync.WaitGroup
	for range 2 {
		for _, candidate := range []struct {
			version string
			status  int
		}{
			{oldVersion, http.StatusNotFound},
			{currentVersion, http.StatusOK},
		} {
			requests.Go(func() {
				<-start
				resp, err := http.Get(ts.URL + base + candidate.version)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != candidate.status {
					t.Errorf("冷缓存并发: version=%s status=%d body=%q err=%v want=%d", candidate.version, resp.StatusCode, raw, err, candidate.status)
				}
				if candidate.status == http.StatusOK {
					if string(raw) != images[0] || !strings.Contains(resp.Header.Get("Cache-Control"), "max-age=300") {
						t.Errorf("当前版本图片/缓存契约错误: body=%q cache=%q", raw, resp.Header.Get("Cache-Control"))
					}
				} else if resp.Header.Get("Cache-Control") != "" {
					t.Error("过期版本不能被浏览器缓存")
				}
			})
		}
	}
	close(start)
	requests.Wait()
	mu.Lock()
	coldLookups, coldDownloads := lookups, downloads
	mu.Unlock()
	if coldLookups < 3 || coldDownloads == 0 {
		t.Fatalf("未覆盖过期版本拒绝及当前版本冷重建: lookups=%d downloads=%d", coldLookups, coldDownloads)
	}
	// 当前版本冷重建成功后，下一请求必须命中字节缓存，不再访问上游。
	resp, err := http.Get(ts.URL + base + currentVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(raw) != images[0] {
		t.Fatalf("冷重建后的热命中: status=%d body=%q err=%v", resp.StatusCode, raw, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if lookups != coldLookups || downloads != coldDownloads {
		t.Fatalf("重建后未命中字节缓存: lookups=%d/%d downloads=%d/%d", lookups, coldLookups, downloads, coldDownloads)
	}
}

func TestEncodeStillListUnambiguous(t *testing.T) {
	a, av := encodeStillList([]string{"https://p/a\nhttps://p/b"})
	b, bv := encodeStillList([]string{"https://p/a", "https://p/b"})
	if av == bv || bytes.Equal(a, b) {
		t.Fatal("不同列表不能因换行分隔符产生相同版本")
	}
	_, reordered := encodeStillList([]string{"https://p/b", "https://p/a"})
	if reordered == bv {
		t.Fatal("列表顺序必须进入版本")
	}
	_, normalized := encodeStillList(stillURLs(metatube.MovieInfo{PreviewImages: []string{
		"", " https://p/a ", "https://p/a", "https://p/b",
	}}))
	if normalized != bv {
		t.Fatal("版本必须使用归一化后的列表")
	}
}
