package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/avatar"
	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// detailTestApp 建库目录与 NFO，返回可用的 App 与媒体库目录。
func detailTestApp(t *testing.T, root, nfoBody string) (*App, string) {
	t.Helper()
	libDir := filepath.Join(root, "media")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(libDir, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	writeFile(t, filepath.Join(libDir, "ABF-018.nfo"), nfoBody)

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "t.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app, libDir
}

// detailServer 起测试服务并完成初始化 + 登录，返回服务与令牌。
func detailServer(t *testing.T, app *App) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)
	post := func(path, body string) {
		t.Helper()
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	post("/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`)
	loginResp, err := http.Post(ts.URL+"/Users/AuthenticateByName", "application/json",
		strings.NewReader(`{"Username":"admin","Pw":"password-1234"}`))
	if err != nil {
		t.Fatal(err)
	}
	var login map[string]any
	json.NewDecoder(loginResp.Body).Decode(&login)
	loginResp.Body.Close()
	token, _ := login["AccessToken"].(string)
	if token == "" {
		t.Fatal("未取得令牌")
	}
	return ts, token
}

// scanOnce 触发一次全库扫描并要求成功。
func scanOnce(t *testing.T, ts *httptest.Server, token string) {
	t.Helper()
	resp, body := embyCall(t, ts, "POST", "/api/admin/scan", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("扫描: %d %v", resp.StatusCode, body)
	}
}

// TestAdminItemDetail 覆盖详情抽屉的数据契约：文件分组、流信息、演员头像可用性、探测状态。
func TestAdminItemDetail(t *testing.T) {
	root := t.TempDir()
	app, libDir := detailTestApp(t, root, `<movie>
  <title>ABF-018</title>
  <plot>剧情简介</plot>
  <num>ABF-018</num>
  <year>2024</year>
  <rating>8.5</rating>
  <mpaa>JP-18+</mpaa>
  <genre>剧情</genre>
  <genre>偶像</genre>
  <studio>Studio X</studio>
  <director>导演甲</director>
  <actor><name>有头像</name><thumb>https://metatube.test/a.jpg</thumb></actor>
  <actor><name>无头像</name></actor>
  <fileinfo><size>12345678</size>
    <streamdetails>
      <video><codec>h264</codec><width>1920</width><height>1080</height><bitrate>5000000</bitrate></video>
      <audio><codec>aac</codec><channels>2</channels><language>jpn</language></audio>
      <subtitle><codec>subrip</codec><language>chi</language><title>简体中文</title></subtitle>
      <subtitle><codec>pgs</codec><language>eng</language><forced>true</forced></subtitle>
    </streamdetails>
  </fileinfo>
</movie>
`)
	if lib, err := app.db.AddLibrary("L", libDir); err != nil || lib.ID == 0 {
		t.Fatalf("建库: %v", err)
	}

	ts, token := detailServer(t, app)
	scanOnce(t, ts, token)

	// 给「有头像」写本地副本 + 索引，验证详情里的头像可用性判定。
	imgBytes := []byte("avatar-bytes")
	if err := os.MkdirAll(app.avatarsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avatar.Path(app.avatarsDir(), "有头像"), imgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.db.SetActorAvatar("有头像", "https://metatube.test/a.jpg", avatar.Tag(imgBytes)); err != nil {
		t.Fatal(err)
	}

	resp, detail := embyCall(t, ts, "GET", "/api/admin/items/1/detail", token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("详情接口: %d %v", resp.StatusCode, detail)
	}

	movie, _ := detail["movie"].(map[string]any)
	if movie["Title"] != "ABF-018" || movie["Number"] != "ABF-018" {
		t.Errorf("movie 字段不对: %v", movie)
	}
	if detail["library_name"] != "L" {
		t.Errorf("library_name = %v", detail["library_name"])
	}

	files, _ := detail["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files 数量 = %d，期望 1", len(files))
	}
	file, _ := files[0].(map[string]any)
	if file["role"] != "main" {
		t.Errorf("唯一文件应为 main，得到 %v", file["role"])
	}
	if file["probed"] != true {
		t.Error("NFO 有 streamdetails 时 probed 应为 true")
	}
	if size, _ := file["size"].(float64); size != 12345678 {
		t.Errorf("主文件体积应取 NFO <fileinfo><size>，得到 %v", file["size"])
	}
	streams, _ := file["streams"].([]any)
	if len(streams) != 4 {
		t.Fatalf("流数量 = %d，期望 4（视频+音频+2 字幕）: %v", len(streams), streams)
	}
	first, _ := streams[0].(map[string]any)
	if first["Type"] != "Video" || first["Width"] != float64(1920) {
		t.Errorf("视频轨参数不对: %v", first)
	}
	// 索引必须与数组下标一致（tsukimi 直接把 Index 当数组下标用）。
	for i, raw := range streams {
		stream, _ := raw.(map[string]any)
		if stream["Index"] != float64(i) {
			t.Errorf("第 %d 条流的 Index = %v，必须等于数组下标", i, stream["Index"])
		}
	}
	// 字幕轨：文本/图形标记正确，且 forced 只在显式标注时为真。
	sub1, _ := streams[2].(map[string]any)
	if sub1["Type"] != "Subtitle" || sub1["IsTextSubtitleStream"] != true || sub1["IsForced"] != false {
		t.Errorf("文本字幕轨不对: %v", sub1)
	}
	// 字幕的 IsDefault 与音视频轨不同：缺省不算默认，避免多条字幕同时声称默认。
	if sub1["IsDefault"] != false {
		t.Errorf("未标注 <default> 的字幕轨不应是默认: %v", sub1["IsDefault"])
	}
	if sub1["DisplayTitle"] != "简体中文" {
		t.Errorf("字幕轨 DisplayTitle 应取 NFO <title>，得到 %v", sub1["DisplayTitle"])
	}
	sub2, _ := streams[3].(map[string]any)
	if sub2["IsTextSubtitleStream"] != false || sub2["IsForced"] != true {
		t.Errorf("图形强迫字幕轨不对: %v", sub2)
	}

	actors, _ := detail["actors"].([]any)
	if len(actors) != 2 {
		t.Fatalf("actors 数量 = %d，期望 2", len(actors))
	}
	byName := map[string]map[string]any{}
	for _, raw := range actors {
		item, _ := raw.(map[string]any)
		byName[item["name"].(string)] = item
	}
	if with := byName["有头像"]; with["has_image"] != true || with["image_tag"] != avatar.Tag(imgBytes) {
		t.Errorf("有本地副本的演员应带 image_tag: %v", with)
	}
	if without := byName["无头像"]; without["has_image"] != false || without["image_tag"] != nil {
		t.Errorf("无本地副本的演员不应有 image_tag: %v", without)
	}
	if url := byName["有头像"]["avatar_url"]; url != "https://metatube.test/a.jpg" {
		t.Errorf("avatar_url 应来自 NFO <thumb>，得到 %v", url)
	}
}

// TestAdminItemDetailNotProbed 未探测的条目应如实报 probed=false（页面显示「未探测」）。
func TestAdminItemDetailNotProbed(t *testing.T) {
	root := t.TempDir()
	app, libDir := detailTestApp(t, root, "<movie><title>未探测</title></movie>\n")
	if _, err := app.db.AddLibrary("L", libDir); err != nil {
		t.Fatal(err)
	}
	ts, token := detailServer(t, app)
	scanOnce(t, ts, token)
	resp, detail := embyCall(t, ts, "GET", "/api/admin/items/1/detail", token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("详情接口: %d %v", resp.StatusCode, detail)
	}
	files, _ := detail["files"].([]any)
	file, _ := files[0].(map[string]any)
	if file["probed"] != false {
		t.Errorf("无 streamdetails 时 probed 应为 false: %v", file)
	}
}

func TestAdminItemDetailNotFound(t *testing.T) {
	root := t.TempDir()
	app, _ := detailTestApp(t, root, "<movie><title>x</title></movie>\n")
	ts, token := detailServer(t, app)
	resp, _ := embyCall(t, ts, "GET", "/api/admin/items/999/detail", token, "")
	if resp.StatusCode != 404 {
		t.Errorf("不存在的影片应 404，得到 %d", resp.StatusCode)
	}
}
