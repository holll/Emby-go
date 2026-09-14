package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// TestCollectionMinMoviesThreshold 合集最低影片数（默认 2）：
// 只有一部影片的 <set> 不出现在合集列表里，直接按 id 也打不开；
// 达到阈值的合集照常列出。
func TestCollectionMinMoviesThreshold(t *testing.T) {
	root := t.TempDir()
	// 三部片：前两部同属「系列A」，第三部独占「孤本」。
	for _, item := range []struct {
		name  string
		title string
		set   string
	}{
		{"a", "A", "系列A"},
		{"b", "B", "系列A"},
		{"c", "C", "孤本"},
	} {
		writeFile(t, filepath.Join(root, item.name+".strm"), "http://media.test/"+item.name+".mp4\n")
		writeFile(t, filepath.Join(root, item.name+".nfo"),
			"<movie><title>"+item.title+"</title><genre>Drama</genre><set><name>"+item.set+"</name></set></movie>\n")
	}

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()

	call := func(method, path, token, body string) (*http.Response, []byte) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, reader)
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}
	if resp, _ := call("POST", "/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d", resp.StatusCode)
	}
	_, authRaw := call("POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`)
	var auth struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil || auth.AccessToken == "" {
		t.Fatalf("登录失败: %v %s", err, authRaw)
	}
	libBody := `{"Name":"AV","Path":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	if resp, _ := call("POST", "/api/admin/libraries", auth.AccessToken, libBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("add library: %d", resp.StatusCode)
	}
	if resp, raw := call("POST", "/api/admin/scan", auth.AccessToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("scan: %d %s", resp.StatusCode, raw)
	}

	get := func(path string) (int, map[string]any) {
		resp, raw := call("GET", path, auth.AccessToken, "")
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		return resp.StatusCode, body
	}

	// 合集列表：只有「系列A」达到 2 部。
	status, list := get("/Users/1/Items?ParentId=boxsets&IncludeItemTypes=BoxSet")
	if status != http.StatusOK {
		t.Fatalf("合集列表: %d", status)
	}
	if total := list["TotalRecordCount"].(float64); total != 1 {
		t.Fatalf("合集数 = %v，期望 1（「孤本」只有一部片）: %v", total, list)
	}
	item := list["Items"].([]any)[0].(map[string]any)
	if item["Name"] != "系列A" {
		t.Fatalf("合集名 = %v，期望 系列A", item["Name"])
	}
	if count := item["ChildCount"].(float64); count != 2 {
		t.Errorf("系列A 影片数 = %v，期望 2", count)
	}

	// 只有一部影片的合集：即使知道 id 也打不开。
	status, _ = get("/Items/" + boxsetID("孤本"))
	if status != http.StatusNotFound {
		t.Errorf("低于阈值的合集详情应 404，实际 %d", status)
	}
	status, detail := get("/Items/" + boxsetID("系列A"))
	if status != http.StatusOK || detail["Type"] != "BoxSet" {
		t.Errorf("达到阈值的合集详情应正常：status=%d body=%v", status, detail)
	}

	// 阈值调成 1：两个合集都出现（设置项即时生效）。
	if err := app.db.SetSetting(settingCollectionMinMovies, "1"); err != nil {
		t.Fatal(err)
	}
	_, list = get("/Users/1/Items?ParentId=boxsets&IncludeItemTypes=BoxSet")
	if total := list["TotalRecordCount"].(float64); total != 2 {
		t.Errorf("阈值 1 时合集数 = %v，期望 2: %v", total, list)
	}
	// Views 里的「合集」文件夹计数也应跟着变。
	_, views := get("/Users/1/Views")
	for _, raw := range views["Items"].([]any) {
		view := raw.(map[string]any)
		if view["Id"] == boxsetViewID {
			unplayed := view["UserData"].(map[string]any)["UnplayedItemCount"].(float64)
			if unplayed != 2 {
				t.Errorf("合集文件夹计数 = %v，期望 2", unplayed)
			}
		}
	}
}
