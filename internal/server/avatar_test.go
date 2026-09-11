package server

import (
	"encoding/json"
	"io"
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

// TestActorAvatarChain 覆盖演员头像链路的四段：NFO → DB 索引 → People[] 契约 → 图片端点。
//
// 头像真源是 NFO 的 <actor><thumb>；本地副本放 avatars/<hash>.webp；
// DB 只作索引。没有本地副本时必须回退到「参演影片海报」的既有占位行为，不能破图。
func TestActorAvatarChain(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "media")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(libDir, "ABF-018.strm"), "http://media.test/ABF-018.mp4\n")
	// 两个演员：一个带 <thumb>，一个没有。
	writeFile(t, filepath.Join(libDir, "ABF-018.nfo"), `<movie>
  <title>ABF-018</title>
  <actor><name>有头像</name><type>Actor</type><thumb>https://metatube.test/actors/a.jpg</thumb></actor>
  <actor><name>无头像</name><type>Actor</type></actor>
</movie>
`)

	dbPath := filepath.Join(root, "t.db")
	app, err := newApp(config.Config{DBPath: dbPath}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)

	post := func(path, body, token string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post("/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d", resp.StatusCode)
	}
	loginResp, _ := http.Post(ts.URL+"/Users/AuthenticateByName", "application/json",
		strings.NewReader(`{"Username":"admin","Pw":"password-1234"}`))
	var login map[string]any
	json.NewDecoder(loginResp.Body).Decode(&login)
	loginResp.Body.Close()
	token, _ := login["AccessToken"].(string)

	if lib, err := app.db.AddLibrary("L", libDir); err != nil || lib.ID == 0 {
		t.Fatalf("建库: %v", err)
	}
	if resp := post("/api/admin/scan", "", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("扫描: %d", resp.StatusCode)
	}

	// 1) 扫库把 NFO 的 <thumb> 带进了 actors 索引。
	stored, err := app.db.ActorAvatar("有头像")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AvatarURL != "https://metatube.test/actors/a.jpg" {
		t.Errorf("actors.avatar_url = %q，期望取自 NFO <thumb>", stored.AvatarURL)
	}
	if noThumb, _ := app.db.ActorAvatar("无头像"); noThumb.AvatarURL != "" {
		t.Errorf("无 <thumb> 的演员不应有头像地址，得到 %q", noThumb.AvatarURL)
	}

	// 2) 写本地副本 + 内容标识（阶段 5 的头像任务负责，这里直接落盘验证读取侧）。
	imgBytes := []byte("fake-webp-avatar-bytes")
	avatarsDir := app.avatarsDir()
	if err := os.MkdirAll(avatarsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avatar.Path(avatarsDir, "有头像"), imgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.db.SetActorAvatar("有头像", "https://metatube.test/actors/a.jpg", avatar.Tag(imgBytes)); err != nil {
		t.Fatal(err)
	}

	// 3) 契约：People[] 带 PrimaryImageTag，客户端才会去请求图片。
	_, detail := embyCall(t, ts, "GET", "/Users/1/Items/1", token, "")
	people, _ := detail["People"].([]any)
	if len(people) != 2 {
		t.Fatalf("People 数量 = %d，期望 2: %v", len(people), detail["People"])
	}
	byTag := map[string]map[string]any{}
	for _, raw := range people {
		person, _ := raw.(map[string]any)
		name, _ := person["Name"].(string)
		byTag[name] = person
	}
	if with := byTag["有头像"]; with["PrimaryImageTag"] != avatar.Tag(imgBytes) {
		t.Errorf("有头像的演员 PrimaryImageTag = %v，期望 %q", with["PrimaryImageTag"], avatar.Tag(imgBytes))
	}
	if without := byTag["无头像"]; without["PrimaryImageTag"] != nil {
		t.Errorf("无本地头像的演员不应带 PrimaryImageTag，得到 %v", without["PrimaryImageTag"])
	}

	// 4) 图片端点：有头像回本地副本；无头像回退参演影片海报（此处无海报 → 404，不破图）。
	personID := entityId("Person", "有头像")
	resp, err := http.Get(ts.URL + "/Items/" + personID + "/Images/Primary")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("头像端点状态 = %d，期望 200", resp.StatusCode)
	}
	if string(body) != string(imgBytes) {
		t.Errorf("头像端点返回的不是本地副本: %q", string(body))
	}

	// 5) 重扫不应抹掉头像索引（扫库只读到姓名，头像是头像任务写的）。
	if resp := post("/api/admin/scan", "", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("重扫: %d", resp.StatusCode)
	}
	again, _ := app.db.ActorAvatar("有头像")
	if again.AvatarTag != avatar.Tag(imgBytes) {
		t.Errorf("重扫后 avatar_tag 被清空: %q", again.AvatarTag)
	}
	if again.AvatarURL == "" {
		t.Error("重扫后 avatar_url 被清空")
	}
}
