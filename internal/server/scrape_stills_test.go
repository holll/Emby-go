package server

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// MT-03: inspect 只暴露受限剧照候选——下标由服务端归一化（去空、去重、截断），
// 地址一律走本服务代理，客户端拿不到上游 URL。
func TestScrapeInspectStills(t *testing.T) {
	app, ts, token := contractEnv(t)
	images := []string{"", "https://p/1.jpg", " https://p/1.jpg ", "https://p/2.jpg"}
	for index := 3; index < 40; index++ {
		images = append(images, "https://p/sample"+strings.Repeat("0", index%3)+string(rune('a'+index%26))+".jpg")
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"provider": "FANZA", "id": "abc00123", "number": "NEW-002", "title": "new title",
		"preview_images": images,
	}})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}

	resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token, `{"provider":"FANZA","id":"abc00123"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inspect: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Stills []struct {
			Index   int    `json:"index"`
			Preview string `json:"preview"`
		} `json:"stills"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Stills) != maxScrapeStills {
		t.Fatalf("剧照条数 = %d，期望截断到 %d", len(out.Stills), maxScrapeStills)
	}
	for index, still := range out.Stills {
		if still.Index != index {
			t.Fatalf("第 %d 条 index = %d，下标必须连续", index, still.Index)
		}
		if !strings.Contains(still.Preview, "kind=still") || !strings.Contains(still.Preview, "index=") {
			t.Fatalf("第 %d 条预览地址不对: %s", index, still.Preview)
		}
	}
	// 去空去重后第一条是 https://p/1.jpg，第二条才是 2.jpg：重复项不能占掉一个下标。
	if !strings.Contains(out.Stills[0].Preview, "index=0") || !strings.Contains(out.Stills[1].Preview, "index=1") {
		t.Fatalf("前两条地址不对: %+v", out.Stills[:2])
	}
}

// MT-03: 剧照代理只接受受限下标，地址由服务端重新解析；解析结果按影片缓存，
// 换下标不重复拉元数据，换影片才重新解析。
func TestScrapeStillProxy(t *testing.T) {
	app, ts, _ := contractEnv(t)
	var mu sync.Mutex
	var lookups []string
	var imageQueries []url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/movies/") {
			mu.Lock()
			lookups = append(lookups, r.URL.RequestURI())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002",
				"preview_images":["https://p/1.jpg","https://p/2.jpg"]}}`))
			return
		}
		mu.Lock()
		imageQueries = append(imageQueries, r.URL.Query())
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
		"kind=still",           // 缺 index
		"kind=still&index=-1",  // 负下标
		"kind=still&index=abc", // 非数字
		"kind=still&index=0&ratio=2",
		"kind=still&index=0&pos=0.5",
	} {
		if status := fetch(query); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d，期望 400", query, status)
		}
	}
	if status := fetch("kind=still&index=2"); status != http.StatusNotFound {
		t.Errorf("越界下标: status = %d，期望 404", status)
	}
	mu.Lock()
	if len(imageQueries) != 0 {
		t.Fatalf("非法参数不应打到图片接口: %v", imageQueries)
	}
	mu.Unlock()

	if status := fetch("kind=still&index=0"); status != http.StatusOK {
		t.Fatalf("index=0: status = %d", status)
	}
	mu.Lock()
	if len(imageQueries) != 1 {
		t.Fatalf("应向上游取一张图: %v", imageQueries)
	}
	first := imageQueries[0]
	if got := first.Get("url"); got != "https://p/1.jpg" {
		t.Fatalf("剧照 url = %q", got)
	}
	if got := first.Get("ratio"); got != "1.7777777777777777" || first.Get("pos") != "0.5" {
		t.Fatalf("剧照应固定 16:9 居中: %v", first)
	}
	mu.Unlock()

	// 同一张图重复请求命中字节缓存，不再打上游。
	if status := fetch("kind=still&index=0"); status != http.StatusOK {
		t.Fatalf("repeat index=0: status = %d", status)
	}
	// 换下标复用已解析的列表，只多取一张图。
	if status := fetch("kind=still&index=1"); status != http.StatusOK {
		t.Fatalf("index=1: status = %d", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lookups) != 1 {
		t.Fatalf("同一影片只应解析一次元数据: %v", lookups)
	}
	if len(imageQueries) != 2 || imageQueries[1].Get("url") != "https://p/2.jpg" {
		t.Fatalf("换下标应取第二张: %v", imageQueries)
	}
}
