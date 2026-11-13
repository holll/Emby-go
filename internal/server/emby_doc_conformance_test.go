package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 本文件锁定「按官方 RestAPI 文档严格对齐」的回归面（API-CONTRACT §4.20）。
// 每个断言都对应 /root/src/EmbyRestApiDocs 里的一条具体契约，不是自造形状。

// TestEmbyDocHeadVariants 图片与 /System/Ping 必须支持 HEAD：
// 客户端用 HEAD 探测图片是否存在以省流量（速查表 §4、ImageService #23–#39）。
func TestEmbyDocHeadVariants(t *testing.T) {
	_, ts, token := contractEnv(t)

	for _, path := range []string{"/Items/1/Images/Primary", "/emby/Items/1/Images/Primary"} {
		resp, body := embyRaw(t, ts, http.MethodHead, path, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD %s: %d %s", path, resp.StatusCode, body)
		}
		if len(body) != 0 {
			t.Fatalf("HEAD %s 不应有响应体，得到 %d 字节", path, len(body))
		}
	}
	// 图片信息 JSON 与其它端点不受影响：文档没有它们的 HEAD。
	if resp, _ := embyRaw(t, ts, http.MethodHead, "/Items/1/Images", token, ""); resp.StatusCode == http.StatusOK {
		t.Fatal("/Items/{Id}/Images 文档无 HEAD，不应返回 200")
	}
}

// TestEmbyDocPublicSystemInfoFields 严格七字段：不含 ProductName（官方模型没有）。
func TestEmbyDocPublicSystemInfoFields(t *testing.T) {
	_, ts, _ := contractEnv(t)
	resp, raw := embyRaw(t, ts, http.MethodGet, "/System/Info/Public", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Info/Public: %d %s", resp.StatusCode, raw)
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	want := []string{"LocalAddress", "LocalAddresses", "WanAddress", "RemoteAddresses", "ServerName", "Version", "Id"}
	if len(info) != len(want) {
		t.Fatalf("PublicSystemInfo 应恰好 %d 字段，实际 %d: %v", len(want), len(info), info)
	}
	for _, key := range want {
		if _, ok := info[key]; !ok {
			t.Errorf("PublicSystemInfo 缺字段 %s: %v", key, info)
		}
	}
	if _, ok := info["ProductName"]; ok {
		t.Error("PublicSystemInfo 不应含私有字段 ProductName")
	}
}

// TestEmbyDocSystemInfoFieldSet 按文档 SystemInfo 逐字段核对存在性。
func TestEmbyDocSystemInfoFieldSet(t *testing.T) {
	_, ts, token := contractEnv(t)
	resp, raw := embyRaw(t, ts, http.MethodGet, "/System/Info", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("System/Info: %d %s", resp.StatusCode, raw)
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"SystemUpdateLevel", "OperatingSystemDisplayName", "PackageName", "HasPendingRestart",
		"IsShuttingDown", "HasImageEnhancers", "OperatingSystem", "SupportsLibraryMonitor",
		"SupportsLocalPortConfiguration", "SupportsWakeServer", "WebSocketPortNumber",
		"CompletedInstallations", "CanSelfRestart", "CanSelfUpdate", "CanLaunchWebBrowser",
		"ProgramDataPath", "ItemsByNamePath", "CachePath", "LogPath", "InternalMetadataPath",
		"TranscodingTempPath", "HttpServerPortNumber", "SupportsHttps", "HttpsPortNumber",
		"HasUpdateAvailable", "SupportsAutoRunAtStartup", "HardwareAccelerationRequiresPremiere",
		"WakeOnLanInfo", "IsInMaintenanceMode", "LocalAddress", "LocalAddresses", "WanAddress",
		"RemoteAddresses", "ServerName", "Version", "Id",
	} {
		if _, ok := info[key]; !ok {
			t.Errorf("SystemInfo 缺字段 %s", key)
		}
	}
}

// TestEmbyDocLoginReturnsSessionInfo 文档 AuthenticationResult 四字段齐全，
// 且 SessionInfo 回显客户端在 X-Emby-Authorization 里声明的设备信息。
func TestEmbyDocLoginReturnsSessionInfo(t *testing.T) {
	_, ts, _ := contractEnv(t)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/Users/AuthenticateByName",
		strings.NewReader(`{"Username":"admin","Pw":"password-1234"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization",
		`Emby Client="Tsukimi", Device="Steam Deck", DeviceId="dev-42", Version="1.2.3"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string         `json:"AccessToken"`
		ServerId    string         `json:"ServerId"`
		User        map[string]any `json:"User"`
		SessionInfo map[string]any `json:"SessionInfo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.AccessToken == "" || result.ServerId == "" || result.User == nil {
		t.Fatalf("AuthenticationResult 前三项缺失: %+v", result)
	}
	if result.SessionInfo == nil {
		t.Fatal("AuthenticationResult 缺 SessionInfo")
	}
	for _, key := range []string{"Id", "UserId", "UserName", "Client", "DeviceName", "DeviceId", "ApplicationVersion", "ServerId"} {
		if _, ok := result.SessionInfo[key]; !ok {
			t.Errorf("SessionInfo 缺字段 %s", key)
		}
	}
	if result.SessionInfo["Client"] != "Tsukimi" || result.SessionInfo["DeviceId"] != "dev-42" {
		t.Errorf("SessionInfo 未回显请求头: %v", result.SessionInfo)
	}
}

// TestEmbyDocPlaybackInfoEnvelope 文档 PlaybackInfoResponse 三个字段。
func TestEmbyDocPlaybackInfoEnvelope(t *testing.T) {
	_, ts, token := contractEnv(t)
	resp, raw := embyRaw(t, ts, http.MethodGet, "/Items/1/PlaybackInfo?UserId=1", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PlaybackInfo: %d %s", resp.StatusCode, raw)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 3 {
		t.Fatalf("PlaybackInfoResponse 应恰好 3 字段，实际 %v", body)
	}
	for _, key := range []string{"MediaSources", "PlaySessionId", "ErrorCode"} {
		if _, ok := body[key]; !ok {
			t.Errorf("PlaybackInfoResponse 缺 %s", key)
		}
	}
}

// TestEmbyDocUserItemDataFields 文档 UserItemDataDto 的 11 个字段。
func TestEmbyDocUserItemDataFields(t *testing.T) {
	_, ts, token := contractEnv(t)
	resp, raw := embyRaw(t, ts, http.MethodGet, "/Users/1/Items/1", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("详情: %d %s", resp.StatusCode, raw)
	}
	var detail struct {
		UserData map[string]any `json:"UserData"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"Rating", "PlayedPercentage", "UnplayedItemCount", "PlaybackPositionTicks",
		"PlayCount", "IsFavorite", "LastPlayedDate", "Played", "Key", "ItemId", "ServerId",
	} {
		if _, ok := detail.UserData[key]; !ok {
			t.Errorf("UserItemDataDto 缺字段 %s: %v", key, detail.UserData)
		}
	}
	if detail.UserData["Key"] != detail.UserData["ItemId"] {
		t.Errorf("Key 应与 ItemId 一致: %v", detail.UserData)
	}
	if rating, ok := detail.UserData["Rating"].(float64); !ok || rating != 0 {
		t.Errorf("未评分时 Rating 应为 0: %v", detail.UserData["Rating"])
	}
}

// TestEmbyDocNoContentEndpointsReturn200 文档把这组端点写成「200（无返回内容）」。
func TestEmbyDocNoContentEndpointsReturn200(t *testing.T) {
	_, ts, token := contractEnv(t)
	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/Sessions/Playing", `{"ItemId":"1","PlaySessionId":"doc-200","PositionTicks":10000000}`},
		{http.MethodPost, "/Sessions/Playing/Progress", `{"ItemId":"1","PlaySessionId":"doc-200","PositionTicks":10000000}`},
		{http.MethodPost, "/Sessions/Playing/Stopped", `{"ItemId":"1","PlaySessionId":"doc-200","PositionTicks":10000000}`},
		{http.MethodPost, "/Sessions/Playing/Ping", `{}`},
		{http.MethodPost, "/Sessions/Capabilities", `{}`},
		{http.MethodPost, "/Sessions/Capabilities/Full", `{}`},
	}
	for _, tc := range cases {
		resp, raw := embyRaw(t, ts, tc.method, tc.path, token, tc.body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s（文档写 200）", tc.path, resp.StatusCode, raw)
		}
		if len(raw) != 0 {
			t.Errorf("%s 不应有响应体，得到 %q", tc.path, raw)
		}
	}
	// 自有契约不受影响：/api/auth/logout 仍是 204。
	if resp, raw := embyRaw(t, ts, http.MethodPost, "/api/auth/logout", token, ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("/api/auth/logout 应保持 204: %d %s", resp.StatusCode, raw)
	}
}

// TestEmbyDocImageTagGrantsStrongCache 文档 ImageService 的 Tag 参数：
// 传入条目 ImageTag 时应返回强缓存响应头（不代表 304——首请求必须拿到字节）。
func TestEmbyDocImageTagGrantsStrongCache(t *testing.T) {
	_, ts, token := contractEnv(t)
	resp, raw := embyRaw(t, ts, http.MethodGet, "/Users/1/Items?IncludeItemTypes=Movie", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表: %d %s", resp.StatusCode, raw)
	}
	var list struct {
		Items []struct {
			ImageTags map[string]any `json:"ImageTags"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatal("列表为空")
	}
	tag, _ := list.Items[0].ImageTags["Primary"].(string)
	if tag == "" {
		t.Fatal("条目缺 ImageTags.Primary")
	}

	resp, raw = embyRaw(t, ts, http.MethodGet, "/Items/1/Images/Primary?tag="+tag, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("带 tag 取图: %d", resp.StatusCode)
	}
	if len(raw) == 0 {
		t.Error("带 tag 的首次请求必须返回图片字节")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("tag 命中应返回强缓存头，得到 %q", cc)
	}

	// tag 不匹配时退回按天过期 + 每次校验。
	other, _ := embyRaw(t, ts, http.MethodGet, "/Items/1/Images/Primary?tag=stale", "", "")
	if cc := other.Header.Get("Cache-Control"); cc != fmt.Sprintf("public, max-age=%d, must-revalidate", int(imageMaxAge.Seconds())) {
		t.Errorf("tag 不匹配应保持可校验缓存，得到 %q", cc)
	}
}
