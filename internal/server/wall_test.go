package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// TestWallLibraryFilter 覆盖媒体墙的库筛选：单独选库只返回该库影片，
// 且与状态/搜索筛选叠加；不传 library_id 时返回全部。
func TestWallLibraryFilter(t *testing.T) {
	root := t.TempDir()
	dirA, dirB := filepath.Join(root, "A"), filepath.Join(root, "B")
	for _, dir := range []string{dirA, dirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A 库：两条可播放；B 库：一条可播放 + 一条 ed2k（不可播放），用于验证与状态叠加。
	writeFile(t, filepath.Join(dirA, "a1.strm"), "http://media.test/a1.mp4\n")
	writeFile(t, filepath.Join(dirA, "a1.nfo"), "<movie><title>A1</title><genre>剧情</genre><studio>Studio X</studio><actor><name>Actor One</name></actor></movie>\n")
	writeFile(t, filepath.Join(dirA, "a2.strm"), "http://media.test/a2.mp4\n")
	writeFile(t, filepath.Join(dirA, "a2.nfo"), "<movie><title>A2</title></movie>\n")
	writeFile(t, filepath.Join(dirB, "b1.strm"), "http://media.test/b1.mp4\n")
	writeFile(t, filepath.Join(dirB, "b1.nfo"), "<movie><title>B1</title></movie>\n")
	writeFile(t, filepath.Join(dirB, "b2.strm"), "ed2k://|file|b2.mp4|1|abc|/\n")

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "t.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)

	post := func(path, body, token string) (*http.Response, []byte) {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, raw
	}
	if resp, _ := post("/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d", resp.StatusCode)
	}
	loginResp, _ := http.Post(ts.URL+"/Users/AuthenticateByName", "application/json",
		strings.NewReader(`{"Username":"admin","Pw":"password-1234"}`))
	var login map[string]any
	json.NewDecoder(loginResp.Body).Decode(&login)
	loginResp.Body.Close()
	token, _ := login["AccessToken"].(string)

	libA, err := app.db.AddLibrary("A库", dirA)
	if err != nil {
		t.Fatal(err)
	}
	libB, err := app.db.AddLibrary("B库", dirB)
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := post("/api/admin/scan", "", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("扫描: %d %s", resp.StatusCode, body)
	}

	count := func(query string) int {
		t.Helper()
		resp, v := embyCall(t, ts, "GET", "/api/admin/items?limit=100"+query, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("items%s: %d %v", query, resp.StatusCode, v)
		}
		total, ok := v["total"].(float64)
		if !ok {
			t.Fatalf("items%s 缺少 total: %v", query, v)
		}
		return int(total)
	}

	idA, idB := strconv.FormatInt(libA.ID, 10), strconv.FormatInt(libB.ID, 10)

	// 不传 status 时只含可播放/手动录入，故 B 库的 ed2k 条目不计入。
	if got := count(""); got != 3 {
		t.Errorf("全部库影片数 = %d，期望 3", got)
	}
	if got := count("&library_id=" + idA); got != 2 {
		t.Errorf("A 库影片数 = %d，期望 2", got)
	}
	if got := count("&library_id=" + idB); got != 1 {
		t.Errorf("B 库影片数 = %d，期望 1", got)
	}
	// 与状态筛选叠加。
	if got := count("&library_id=" + idB + "&status=success"); got != 1 {
		t.Errorf("B 库可播放影片数 = %d，期望 1", got)
	}
	if got := count("&library_id=" + idB + "&status=incompatible"); got != 1 {
		t.Errorf("B 库不兼容影片数 = %d，期望 1", got)
	}
	if got := count("&library_id=" + idA + "&status=incompatible"); got != 0 {
		t.Errorf("A 库不兼容影片数 = %d，期望 0", got)
	}
	// 与搜索叠加。
	if got := count("&library_id=" + idA + "&search=A1"); got != 1 {
		t.Errorf("A 库搜索 A1 结果数 = %d，期望 1", got)
	}
	if got := count("&library_id=" + idB + "&search=A1"); got != 0 {
		t.Errorf("B 库搜索 A1 结果数 = %d，期望 0（跨库不应命中）", got)
	}
	// 不存在的库：返回空列表而不是错误。
	if got := count("&library_id=9999"); got != 0 {
		t.Errorf("不存在库的结果数 = %d，期望 0", got)
	}

	// 详情抽屉的实体跳转：按类型/厂商/演员过滤，并且能与库筛选叠加。
	if got := count("&genre=" + url.QueryEscape("剧情")); got != 1 {
		t.Errorf("按类型过滤 = %d，期望 1", got)
	}
	if got := count("&studio=" + url.QueryEscape("Studio X")); got != 1 {
		t.Errorf("按厂商过滤 = %d，期望 1", got)
	}
	if got := count("&person=" + url.QueryEscape("Actor One")); got != 1 {
		t.Errorf("按演员过滤 = %d，期望 1", got)
	}
	if got := count("&person=" + url.QueryEscape("Actor One") + "&library_id=" + idB); got != 0 {
		t.Errorf("演员在 B 库无作品，结果数 = %d，期望 0", got)
	}
	if got := count("&genre=" + url.QueryEscape("不存在的类型")); got != 0 {
		t.Errorf("不存在的类型结果数 = %d，期望 0", got)
	}
}
