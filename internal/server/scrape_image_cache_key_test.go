package server

import (
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 图片代理的缓存 key 必须对 (kind, provider, id, 裁切参数) 单射。provider 与 id
// 来自查询参数、属于不可信输入，任何字符都可能出现：直接拼 ':' 会让
// (provider="a:b", id="c") 与 (provider="a", id="b:c") 撞成同一条缓存。
func TestScrapeImageCacheKeyDistinguishesIdentity(t *testing.T) {
	app, ts, _ := contractEnv(t)
	var mu sync.Mutex
	seen := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI())
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v1/movies/") {
			_, _ = w.Write([]byte(`{"data":{"id":"x","provider":"y","number":"N","title":"T",
				"preview_images":["https://p/1.jpg"]}}`))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_ = jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	// 两对身份拼接后形状相同，必须落到不同的缓存条目。
	identities := [][2]string{{"FANZA", "abc:def"}, {"FANZA:abc", "def"}}
	fetch := func(query string) {
		t.Helper()
		resp, err := http.Get(ts.URL + "/api/admin/scrape/image?" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d", query, resp.StatusCode)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
	}
	count := func(prefix string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, uri := range seen {
			if strings.HasPrefix(uri, prefix) {
				n++
			}
		}
		return n
	}

	for _, identity := range identities {
		fetch("kind=primary&provider=" + identity[0] + "&id=" + identity[1])
	}
	if n := count("/v1/images/primary/"); n != 2 {
		t.Errorf("不同 (provider,id) 的图片必须各自取图，上游收到 %d 次: %v", n, seen)
	}
	for _, identity := range identities {
		fetch("kind=still&index=0&provider=" + identity[0] + "&id=" + identity[1])
	}
	if n := count("/v1/movies/"); n != 2 {
		t.Errorf("不同 (provider,id) 的剧照列表必须各自解析，上游收到 %d 次: %v", n, seen)
	}
}
