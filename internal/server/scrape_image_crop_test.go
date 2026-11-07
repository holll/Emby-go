package server

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// MT-02: 图片代理只在 primary 上接受 ratio/pos，越界与 NaN 一律在边界拒绝；
// 裁切参数必须进缓存 key，换位置不能拿到上一张裁好的图。
func TestScrapeImageCropParams(t *testing.T) {
	app, ts, _ := contractEnv(t)
	var mu sync.Mutex
	seen := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	fetch := func(query string) int {
		t.Helper()
		resp, err := http.Get(ts.URL + "/api/admin/scrape/image?provider=FANZA&id=abc00123&" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, query := range []string{
		"kind=primary&ratio=",
		"kind=primary&pos=%20",
		"kind=primary&ratio",
		"kind=thumb&pos=",
		"kind=backdrop&ratio=%20",
		"kind=still&index=0&ratio=",
		"kind=still&index=0&pos=%20",
		"kind=primary&ratio=0.001",     // 低于上游裁切下限
		"kind=primary&ratio=101",       // 高于上限
		"kind=primary&pos=-0.1",        // 位置越界
		"kind=primary&pos=1.1",         // 位置越界
		"kind=primary&pos=NaN",         // 非有限值
		"kind=thumb&pos=0.5",           // 宽幅图不接受裁切参数
		"kind=backdrop&ratio=0.666666", // 同上
	} {
		if status := fetch(query); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, 期望 400", query, status)
		}
	}
	mu.Lock()
	if len(seen) != 0 {
		t.Errorf("非法参数不应打到上游: %v", seen)
	}
	mu.Unlock()

	// 合法参数原样转发，并进缓存 key：同参数命中缓存，换位置必须重新打上游。
	if status := fetch("kind=primary&pos=0.3"); status != http.StatusOK {
		t.Fatalf("pos=0.3: status = %d", status)
	}
	if status := fetch("kind=primary&pos=0.3"); status != http.StatusOK {
		t.Fatalf("repeat pos=0.3: status = %d", status)
	}
	mu.Lock()
	if len(seen) != 1 || !strings.Contains(seen[0], "pos=0.3") {
		t.Fatalf("同参数应命中缓存且带上游 pos: %v", seen)
	}
	mu.Unlock()
	if status := fetch("kind=primary&pos=0.7"); status != http.StatusOK {
		t.Fatalf("pos=0.7: status = %d", status)
	}
	mu.Lock()
	if len(seen) != 2 || !strings.Contains(seen[1], "pos=0.7") {
		t.Fatalf("换位置后应重新取图: %v", seen)
	}
	mu.Unlock()
	// 不带裁切参数时保持旧 URL（上游自己按默认策略裁）。
	if status := fetch("kind=primary"); status != http.StatusOK {
		t.Fatalf("no crop: status = %d", status)
	}
	mu.Lock()
	if len(seen) != 3 || strings.Contains(seen[2], "pos=") || strings.Contains(seen[2], "ratio=") {
		t.Fatalf("缺省不应带裁切参数: %v", seen)
	}
	mu.Unlock()
}

// MT-02: inspect 的主海报预览要带上将写入的裁切位置，其他类型不带。
func TestScrapeInspectPosterCarriesPosition(t *testing.T) {
	app, ts, token := contractEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}}`))
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	inspect := func(body string) map[string]any {
		t.Helper()
		resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("inspect %s: %d %s", body, resp.StatusCode, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	withPosition := inspect(`{"provider":"FANZA","id":"abc00123","position":0.3}`)
	poster, _ := withPosition["poster"].(string)
	if !strings.Contains(poster, "kind=primary") || !strings.Contains(poster, "pos=0.3") {
		t.Errorf("主海报预览缺少裁切位置: %q", poster)
	}
	images, _ := withPosition["images"].([]any)
	for _, item := range images {
		entry, _ := item.(map[string]any)
		kind, _ := entry["kind"].(string)
		preview, _ := entry["preview"].(string)
		switch kind {
		case "primary":
			if !strings.Contains(preview, "pos=0.3") {
				t.Errorf("主海报落位预览缺少裁切位置: %q", preview)
			}
		default:
			if strings.Contains(preview, "pos=") {
				t.Errorf("%s 不应带裁切位置: %q", kind, preview)
			}
		}
	}

	without := inspect(`{"provider":"FANZA","id":"abc00123"}`)
	if poster, _ := without["poster"].(string); strings.Contains(poster, "pos=") {
		t.Errorf("缺省不应带裁切位置: %q", poster)
	}
	resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token,
		`{"provider":"FANZA","id":"abc00123","position":1.5}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "position") {
		t.Fatalf("越界 position 应 400: %d %s", resp.StatusCode, raw)
	}
}

// MT-02: 确认写入时把裁切位置带到主海报下载，宽幅图不受影响。
func TestScrapeConfirmForwardsPosition(t *testing.T) {
	app, ts, token := contractEnv(t)
	var mu sync.Mutex
	imageQueries := map[string]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/images/") {
			kind := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/images/"), "/")[0]
			mu.Lock()
			imageQueries[kind] = r.URL.RawQuery
			mu.Unlock()
			w.Header().Set("Content-Type", "image/jpeg")
			_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}}`))
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape", token,
		`{"provider":"FANZA","id":"abc00123","overwrite":true,"position":0.3}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d %s", resp.StatusCode, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := imageQueries["primary"]; !strings.Contains(got, "pos=0.3") {
		t.Errorf("主海报应带 pos=0.3: %q", got)
	}
	if got := imageQueries["thumb"]; strings.Contains(got, "pos=") {
		t.Errorf("landscape 不应带 pos: %q", got)
	}
	if got := imageQueries["backdrop"]; strings.Contains(got, "pos=") {
		t.Errorf("fanart 不应带 pos: %q", got)
	}
}
