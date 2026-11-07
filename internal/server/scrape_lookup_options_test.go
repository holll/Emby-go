package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"emby-go/internal/scraper"
)

// MT-01：Update 选项只在精准查询路径上生效（lazy=false），省略时保持缓存优先。
func TestScrapeLookupOptionsCarryUpdate(t *testing.T) {
	app, ts, token := contractEnv(t)
	requests := make(chan *http.Request, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}}`))
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}

	// 确认写入会顺带下载图片，这里只关心 MetaTube 详情请求。
	nextLookup := func() *http.Request {
		t.Helper()
		for i := 0; i < 8; i++ {
			r := <-requests
			if strings.HasPrefix(r.URL.Path, "/v1/movies/") {
				return r
			}
		}
		t.Fatal("no MetaTube lookup request")
		return nil
	}
	inspect := func(body string) *http.Request {
		t.Helper()
		resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape/inspect", token, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("inspect %s: %d %s", body, resp.StatusCode, raw)
		}
		return nextLookup()
	}
	if got := inspect(`{"provider":"FANZA","id":"abc00123"}`); got.URL.Query().Get("lazy") != "true" {
		t.Fatalf("omitted update must keep cache-first: %s", got.URL)
	}
	if got := inspect(`{"provider":"FANZA","id":"abc00123","update":false}`); got.URL.Query().Get("lazy") != "true" {
		t.Fatalf("update=false must keep cache-first: %s", got.URL)
	}
	if got := inspect(`{"provider":"FANZA","id":"abc00123","update":true}`); got.URL.Query().Get("lazy") != "false" {
		t.Fatalf("update=true must refresh: %s", got.URL)
	}

	resp, raw := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape", token, `{"provider":"FANZA","id":"abc00123","overwrite":true,"update":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm: %d %s", resp.StatusCode, raw)
	}
	if got := nextLookup(); got.URL.Query().Get("lazy") != "false" {
		t.Fatalf("confirm must reuse the refresh option: %s", got.URL)
	}
	movie, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfo, err := os.ReadFile(movie.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	// 身份只写 provider/id；Update 是请求选项，不得写进 NFO。
	if !strings.Contains(string(nfo), "FANZA:abc00123") || strings.Contains(string(nfo), ":true") {
		t.Fatalf("NFO identity = %s", nfo)
	}

	// 预览：已存标记带 Update 时，候选携带该选项，前端才能把它转发给 inspect/confirm。
	previewResp, previewRaw := embyRaw(t, ts, "GET", "/api/admin/items/1/scrape/preview?metatube_id="+url.QueryEscape("FANZA:abc00123:0:true"), token, "")
	if previewResp.StatusCode != http.StatusOK {
		t.Fatalf("preview: %d %s", previewResp.StatusCode, previewRaw)
	}
	var preview scraper.PreviewResult
	if err := json.Unmarshal(previewRaw, &preview); err != nil {
		t.Fatal(err)
	}
	if got := nextLookup(); got.URL.Query().Get("lazy") != "false" {
		t.Fatalf("preview must refresh: %s", got.URL)
	}
	if len(preview.Candidates) != 1 || preview.Candidates[0].Update == nil || !*preview.Candidates[0].Update {
		t.Fatalf("preview candidate must carry update: %+v", preview.Candidates)
	}
	if preview.Query != "FANZA:abc00123:0:true" {
		t.Fatalf("preview query must keep the suffix: %q", preview.Query)
	}
}
