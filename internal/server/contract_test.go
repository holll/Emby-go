package server

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// 客户端契约校验（需求 R5）。
//
// 依据是仓库 src/ 下的参考源码，其中 tsukimi（Rust）用 serde 严格反序列化：
// **非 Option 字段缺失即整包解析失败**，是最严的一档；openemby_tv（Kotlin）用
// Gson + 默认值，缺字段基本无感；iPlay 依赖 ImageTags/UserData/MediaStreams 非空。
// 本文件把这些强依赖钉成断言，避免后续改动把契约改坏。

// tsukimiMediaStream 只声明 tsukimi 视为**必填**的字段（见 client/structs.rs）。
type tsukimiMediaStream struct {
	StreamType string `json:"Type"`
	Index      int64  `json:"Index"`
	IsExternal bool   `json:"IsExternal"`
}

type tsukimiMediaSource struct {
	ID           string               `json:"Id"`
	MediaStreams []tsukimiMediaStream `json:"MediaStreams"`
	DirectStream string               `json:"DirectStreamUrl"`
	Chapters     []map[string]any     `json:"Chapters"`
}

type tsukimiPolicy struct {
	IsAdministrator bool `json:"IsAdministrator"`
}

type tsukimiUser struct {
	ID     string        `json:"Id"`
	Policy tsukimiPolicy `json:"Policy"`
}

type tsukimiLoginResponse struct {
	User        tsukimiUser `json:"User"`
	AccessToken string      `json:"AccessToken"`
}

type tsukimiPlayback struct {
	MediaSources  []tsukimiMediaSource `json:"MediaSources"`
	PlaySessionID string               `json:"PlaySessionId"`
}

// contractEnv 起一个带一部可播影片（含海报、演员、字幕轨）的测试服务。
func contractEnv(t *testing.T) (*App, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "media")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(library, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	writeFile(t, filepath.Join(library, "ABF-018.nfo"), `<movie>
  <title>契约片</title>
  <num>ABF-018</num>
  <year>2024</year>
  <premiered>2024-03-05</premiered>
  <genre>剧情</genre>
  <actor><name>演员甲</name><thumb>https://x/a.jpg</thumb></actor>
  <fileinfo><size>1000</size>
    <streamdetails>
      <video><codec>h264</codec><width>1920</width><height>1080</height><bitrate>5000000</bitrate></video>
      <audio><codec>aac</codec></audio>
      <subtitle><codec>subrip</codec><language>chi</language></subtitle>
    </streamdetails>
  </fileinfo>
</movie>
`)
	// 真实 JPEG 海报：ImageTags.Primary 只在存在图片文件时才有值。
	poster, err := os.Create(filepath.Join(library, "poster.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, A: 255})
	if err := jpeg.Encode(poster, img, nil); err != nil {
		t.Fatal(err)
	}
	poster.Close()

	app, err := newApp(config.Config{DBPath: filepath.Join(root, "t.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if _, err := app.db.AddLibrary("L", library); err != nil {
		t.Fatal(err)
	}
	ts, token := detailServer(t, app)
	scanOnce(t, ts, token)
	return app, ts, token
}

// embyRaw 发一次请求并返回状态与原始 body（契约校验要看原始 JSON 形状）。
func embyRaw(t *testing.T, ts *httptest.Server, method, path, token, body string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
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

// TestContractLoginResponse 登录响应必须满足 tsukimi 的严格反序列化：
// AccessToken 存在、User.Id 存在、User.Policy.IsAdministrator 是布尔值。
func TestContractLoginResponse(t *testing.T) {
	_, ts, token := contractEnv(t)

	resp, raw := embyRaw(t, ts, "POST", "/Users/AuthenticateByName", "",
		`{"Username":"admin","Pw":"password-1234"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录: %d %s", resp.StatusCode, raw)
	}
	var login tsukimiLoginResponse
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatalf("严格解析失败（Policy 不能是空对象）: %v\n%s", err, raw)
	}
	if login.AccessToken == "" {
		t.Error("AccessToken 不能为空")
	}
	if login.User.ID == "" {
		t.Error("User.Id 不能为空")
	}
	if !login.User.Policy.IsAdministrator {
		t.Error("单管理员服务应返回 Policy.IsAdministrator=true")
	}

	// GET /Users/{uid}：tsukimi 登录后立刻调它再读一次 Policy。
	resp, raw = embyRaw(t, ts, "GET", "/Users/1", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Users/1: %d %s", resp.StatusCode, raw)
	}
	var user tsukimiUser
	if err := json.Unmarshal(raw, &user); err != nil {
		t.Fatalf("Users/1 严格解析失败: %v\n%s", err, raw)
	}
	if !user.Policy.IsAdministrator {
		t.Error("/Users/1 的 Policy.IsAdministrator 应为 true")
	}
	// /Users/Me 同源同形。
	if resp, raw := embyRaw(t, ts, "GET", "/Users/Me", token, ""); resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(raw), `"IsAdministrator":true`) {
		t.Errorf("/Users/Me 形状不对: %d %s", resp.StatusCode, raw)
	}
}

// TestContractPlaybackInfo 播放信息必须满足最严客户端：
// MediaSources[0].Id/MediaStreams 非空、每条流有 Type/Index/IsExternal 且 Index 与下标一致、
// DirectStreamUrl 是**不含 /emby 前缀的相对路径**、PlaySessionId 存在、Chapters 是数组。
func TestContractPlaybackInfo(t *testing.T) {
	_, ts, token := contractEnv(t)

	resp, raw := embyRaw(t, ts, "GET", "/Users/1/Items/1/PlaybackInfo", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PlaybackInfo: %d %s", resp.StatusCode, raw)
	}
	var playback tsukimiPlayback
	if err := json.Unmarshal(raw, &playback); err != nil {
		t.Fatalf("PlaybackInfo 严格解析失败: %v\n%s", err, raw)
	}
	if len(playback.MediaSources) == 0 {
		t.Fatal("MediaSources 不能为空（iPlay 需要 MediaSources[0]）")
	}
	source := playback.MediaSources[0]
	if source.ID == "" {
		t.Error("MediaSource.Id 不能为空（tsukimi 非 Option）")
	}
	if len(source.MediaStreams) == 0 {
		t.Fatal("MediaStreams 不能为空（iPlay 需要非空且可播）")
	}
	for i, stream := range source.MediaStreams {
		if stream.StreamType == "" {
			t.Errorf("第 %d 条流缺 Type（tsukimi 非 Option）", i)
		}
		// tsukimi 直接把 Index 当数组下标用，错位会导致取错轨道。
		if stream.Index != int64(i) {
			t.Errorf("第 %d 条流 Index=%d，必须等于数组下标", i, stream.Index)
		}
	}
	if playback.PlaySessionID == "" {
		t.Error("PlaySessionId 不能为空（openemby_tv 回传进度时必带）")
	}
	// openemby_tv 从 DirectStreamUrl 起播，路径必须是**不含 /emby 前缀的相对路径**，
	// 因为它的 base 已经带了 /emby，再带一次会拼成 /emby/emby/...
	if !strings.HasPrefix(source.DirectStream, "/") || strings.HasPrefix(source.DirectStream, "/emby") {
		t.Errorf("DirectStreamUrl 应为不含 /emby 前缀的相对路径，得到 %q", source.DirectStream)
	}
	if source.Chapters == nil {
		t.Error("Chapters 应是数组（可为空），不能缺失")
	}

	// 兼容入口：POST /Items/{id}/PlaybackInfo 同样返回该形状。
	if resp, raw := embyRaw(t, ts, "POST", "/Items/1/PlaybackInfo", token, `{}`); resp.StatusCode != http.StatusOK {
		t.Errorf("POST PlaybackInfo: %d %s", resp.StatusCode, raw)
	}
}

// TestContractItemShape 列表/详情的基本形状：TotalRecordCount、UserData 恒存在、
// ImageTags/BackdropImageTags 形状、日期为 RFC3339。
func TestContractItemShape(t *testing.T) {
	_, ts, token := contractEnv(t)

	resp, raw := embyRaw(t, ts, "GET", "/Users/1/Items?IncludeItemTypes=Movie&Recursive=true", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Items: %d %s", resp.StatusCode, raw)
	}
	var list struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount *int             `json:"TotalRecordCount"`
		StartIndex       *int             `json:"StartIndex"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	// tsukimi 把 TotalRecordCount 当 u32 非 Option 读：缺失即整包解析失败。
	if list.TotalRecordCount == nil {
		t.Fatal("TotalRecordCount 缺失（tsukimi 非 Option）")
	}
	if list.StartIndex == nil {
		t.Error("StartIndex 缺失")
	}
	if len(list.Items) == 0 {
		t.Fatal("应有 1 条影片")
	}
	item := list.Items[0]
	if item["UserData"] == nil {
		t.Error("UserData 必须恒存在（iPlay 会直接取字段）")
	}
	if _, ok := item["BackdropImageTags"].([]any); !ok {
		t.Errorf("BackdropImageTags 应是数组: %#v", item["BackdropImageTags"])
	}
	if _, ok := item["ImageTags"].(map[string]any); !ok {
		t.Errorf("ImageTags 应是对象: %#v", item["ImageTags"])
	}
	if item["Type"] != "Movie" || item["MediaType"] != "Video" {
		t.Errorf("Type/MediaType 不对: %v/%v", item["Type"], item["MediaType"])
	}
	if item["Id"] == nil || item["ServerId"] == nil {
		t.Error("Id/ServerId 必须存在")
	}
	// 日期一律 RFC3339（客户端按 DateTime 解析）。
	for _, key := range []string{"DateCreated", "PremiereDate"} {
		value, _ := item[key].(string)
		if value == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Errorf("%s 不是 RFC3339: %q", key, value)
		}
	}

	// 详情（openemby_tv 会带 ExcludeFields 请求同一个端点）。
	resp, raw = embyRaw(t, ts, "GET",
		"/Users/1/Items/1?fields=ShareLevel&ExcludeFields=VideoChapters,VideoMediaSources,MediaStreams", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("详情: %d %s", resp.StatusCode, raw)
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["UserData"] == nil || detail["People"] == nil {
		t.Errorf("详情缺 UserData/People: %v", detail)
	}
	people, _ := detail["People"].([]any)
	if len(people) == 0 {
		t.Fatal("People 不应为空")
	}
	person, _ := people[0].(map[string]any)
	for _, key := range []string{"Name", "Id", "Type"} {
		if person[key] == nil {
			t.Errorf("People 元素缺 %s: %v", key, person)
		}
	}
}

// TestContractTokenCarriers 三种令牌携带方式都要能用：
// 请求头 X-Emby-Token、query X-Emby-Token（部分客户端走 URL）、query api_key。
func TestContractTokenCarriers(t *testing.T) {
	_, ts, token := contractEnv(t)

	for _, tc := range []struct{ path, header string }{
		{"/Users/1/Items/1", token},
		{"/Users/1/Items/1?X-Emby-Token=" + token, ""},
		{"/Users/1/Items/1?api_key=" + token, ""},
	} {
		resp, raw := embyRaw(t, ts, "GET", tc.path, tc.header, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s -> %d %s", tc.path, resp.StatusCode, raw)
		}
	}
	// 无令牌必须 401，不能静默放行。
	if resp, _ := embyRaw(t, ts, "GET", "/Users/1/Items/1", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无令牌应 401，得到 %d", resp.StatusCode)
	}
}

// TestContractImageURLs 图片地址与真实 Emby 同形：Items/{id}/Images/{Type}?tag=&maxWidth=。
func TestContractImageURLs(t *testing.T) {
	_, ts, token := contractEnv(t)

	_, raw := embyRaw(t, ts, "GET", "/Users/1/Items?IncludeItemTypes=Movie&Recursive=true", token, "")
	var list struct {
		Items []map[string]any `json:"Items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	tags, _ := list.Items[0]["ImageTags"].(map[string]any)
	primary, _ := tags["Primary"].(string)
	if primary == "" {
		t.Fatal("有海报的条目应返回 ImageTags.Primary")
	}
	// tag 必须是稳定且随内容变化的标识（本实现是 36 进制 mtime）。
	if !regexp.MustCompile(`^[0-9a-z]+$`).MatchString(primary) {
		t.Errorf("PrimaryImageTag 形状不对: %q", primary)
	}
	resp, body := embyRaw(t, ts, "GET", "/Items/1/Images/Primary?maxWidth=300&tag="+primary, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取图: %d", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Error("图片内容为空")
	}
}

// TestContractNoEmbyPrefixInURLs 所有播放相关 URL 都必须是相对路径，
// 由客户端自行拼接 base（openemby_tv 的 base 已含 /emby）。
func TestContractNoEmbyPrefixInURLs(t *testing.T) {
	_, ts, token := contractEnv(t)

	_, raw := embyRaw(t, ts, "GET", "/emby/Users/1/Items/1/PlaybackInfo", token, "")
	var playback tsukimiPlayback
	if err := json.Unmarshal(raw, &playback); err != nil {
		t.Fatal(err)
	}
	if len(playback.MediaSources) == 0 {
		t.Fatal("MediaSources 为空")
	}
	direct := playback.MediaSources[0].DirectStream
	if strings.HasPrefix(direct, "/emby") {
		t.Errorf("即使请求走了 /emby 前缀，返回的 URL 也不能带它: %q", direct)
	}
	if strings.HasPrefix(direct, "http") {
		t.Errorf("返回绝对地址会让客户端在反向代理/多域名场景下取不到内容: %q", direct)
	}
}

// TestScrapeFailureReleasesNFOGate 刮削失败/结束后必须释放 NFO 写入通道，
// 否则扫库、探测、后续刮削会被永久拒绝（只能重启恢复）。
//
// 用「MetaTube 不可达」制造失败：连接被立即拒绝，熔断会很快收尾。
func TestScrapeFailureReleasesNFOGate(t *testing.T) {
	app, ts, token := contractEnv(t)

	// 指向一个没人监听的端口，保证搜索必然失败。
	settings := `{"metatube_url":"http://127.0.0.1:1","metatube_token":"x","timeout_seconds":2,"concurrency":1,"download_images":false}`
	if resp, raw := embyRaw(t, ts, "PUT", "/api/admin/scrape/settings", token, settings); resp.StatusCode != http.StatusOK {
		t.Fatalf("保存刮削配置: %d %s", resp.StatusCode, raw)
	}

	resp, raw := embyRaw(t, ts, "POST", "/api/admin/scrape/run", token, `{"only_missing":false}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("启动刮削: %d %s", resp.StatusCode, raw)
	}

	// 等任务收尾（失败会很快）。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, body := embyRaw(t, ts, "GET", "/api/admin/scrape/progress", token, "")
		var progress struct {
			Running bool `json:"running"`
			Failed  int  `json:"failed"`
		}
		if err := json.Unmarshal(body, &progress); err == nil && !progress.Running {
			if progress.Failed == 0 {
				t.Fatalf("MetaTube 不可达时应记为失败: %s", body)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 关键断言：通道已释放，扫库能正常启动（不会被「刮削正在进行中」挡住）。
	if resp, raw := embyRaw(t, ts, "POST", "/api/admin/scan", token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("刮削结束后扫库应可启动，得到 %d %s", resp.StatusCode, raw)
	}
	if app.nfoBusyOwner() != "" {
		t.Errorf("任务结束后 NFO 通道应空闲，当前占用者=%q", app.nfoBusyOwner())
	}

	// 单条刮削失败路径同样要释放（服务端重新取详情失败 → 返回错误）。
	if resp, _ := embyRaw(t, ts, "POST", "/api/admin/items/1/scrape", token,
		`{"provider":"p","id":"i","overwrite":false}`); resp.StatusCode == http.StatusOK {
		t.Error("MetaTube 不可达时单条刮削应报错")
	}
	if app.nfoBusyOwner() != "" {
		t.Errorf("单条刮削失败后 NFO 通道应空闲，当前占用者=%q", app.nfoBusyOwner())
	}
}
