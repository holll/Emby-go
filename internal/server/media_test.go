package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/nfo"
	"emby-go/internal/probe"
)

// TestVideoRange 覆盖动态范围判定：必须以色彩传输特性为准，不能凭位深猜。
func TestVideoRange(t *testing.T) {
	cases := []struct {
		name     string
		transfer string
		primarie string
		want     string
	}{
		{"HDR10 (PQ)", "smpte2084", "bt2020", "HDR 10"},
		{"HLG", "arib-std-b67", "bt2020", "HLG"},
		{"BT.2020 传输", "bt2020-10", "bt2020", "HDR 10"},
		{"BT.709 即 SDR", "bt709", "bt709", "SDR"},
		{"BT.601 即 SDR", "smpte170m", "smpte170m", "SDR"},
		// 传输特性缺失时退看基色
		{"仅 BT.2020 基色", "", "bt2020", "HDR 10"},
		{"仅 BT.709 基色", "", "bt709", ""},
		// 两者都缺 → 宁可不报也不给错值
		{"无色彩信息", "", "", ""},
		{"未知传输特性", "some-future-thing", "bt709", ""},
	}
	for _, tc := range cases {
		video := &nfo.VideoStream{ColorTransfer: tc.transfer, ColorPrimaries: tc.primarie}
		if got := videoRange(video); got != tc.want {
			t.Errorf("%s: videoRange(transfer=%q, primaries=%q) = %q, want %q",
				tc.name, tc.transfer, tc.primarie, got, tc.want)
		}
	}
	// 10bit 但传输特性为 BT.709 → 必须判 SDR，不得因位深误判 HDR。
	tenBitSDR := &nfo.VideoStream{BitDepth: 10, PixelFormat: "yuv420p10le", ColorTransfer: "bt709"}
	if got := videoRange(tenBitSDR); got != "SDR" {
		t.Errorf("10bit SDR 片源 = %q, want SDR（位深不得用于判定动态范围）", got)
	}
}

// TestStreamDisplayHelpers 覆盖客户端展示字段的拼装。
func TestStreamDisplayHelpers(t *testing.T) {
	if got := videoDisplayTitle(&nfo.VideoStream{Width: 1920, Height: 1080, Codec: "hevc", Profile: "Main 10"}); got != "1080p HEVC" {
		t.Errorf("videoDisplayTitle = %q, want 1080p HEVC（Profile 不出现在标题里）", got)
	}
	// HDR 轨把 VideoRange 拼进标题（实测 "4K HDR 10 HEVC"）
	if got := videoDisplayTitle(&nfo.VideoStream{Width: 3840, Height: 2160, Codec: "hevc", Profile: "Main 10", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020"}); got != "4K HDR 10 HEVC" {
		t.Errorf("videoDisplayTitle = %q, want 4K HDR 10 HEVC", got)
	}
	// SDR 轨不拼 VideoRange（实测 "1080p H264"）
	if got := videoDisplayTitle(&nfo.VideoStream{Width: 1440, Height: 1080, Codec: "h264", Profile: "Main", ColorTransfer: "bt709"}); got != "1080p H264" {
		t.Errorf("videoDisplayTitle = %q, want 1080p H264", got)
	}
	if got := videoDisplayTitle(&nfo.VideoStream{Width: 320, Height: 240, Codec: "h264", Profile: "High"}); got != "240p H264" {
		t.Errorf("videoDisplayTitle = %q, want 240p H264", got)
	}
	// 缺项自动跳过，不应出现多余空格
	if got := videoDisplayTitle(&nfo.VideoStream{Height: 720, Codec: "h264"}); got != "720p H264" {
		t.Errorf("videoDisplayTitle = %q", got)
	}
	if got := videoDisplayTitle(&nfo.VideoStream{}); got != "" {
		t.Errorf("空视频轨 title = %q, want 空", got)
	}
	// 实测 "Japanese AAC stereo (默认)"：语言 + 编码 + 声道布局 + 默认标记（AAC 的 LC 丢弃）
	if got := audioDisplayTitle(&nfo.AudioStream{Codec: "aac", Profile: "LC", ChannelLayout: "stereo", Language: "jpn", Default: "True"}); got != "Japanese AAC stereo (默认)" {
		t.Errorf("audioDisplayTitle = %q, want Japanese AAC stereo (默认)", got)
	}
	// 实测 "English DTS-HD MA 7.1"：DTS 系用 Profile 当编码名，非默认轨无标记
	if got := audioDisplayTitle(&nfo.AudioStream{Codec: "dts", Profile: "DTS-HD MA", ChannelLayout: "7.1", Language: "eng", Default: "False"}); got != "English DTS-HD MA 7.1" {
		t.Errorf("audioDisplayTitle = %q, want English DTS-HD MA 7.1", got)
	}
	// 实测 "English TRUEHD 7.1 (默认)"
	if got := audioDisplayTitle(&nfo.AudioStream{Codec: "truehd", ChannelLayout: "7.1", Language: "eng", Default: "True"}); got != "English TRUEHD 7.1 (默认)" {
		t.Errorf("audioDisplayTitle = %q, want English TRUEHD 7.1 (默认)", got)
	}
	// 无 channellayout 时用声道数兜底
	if got := audioDisplayTitle(&nfo.AudioStream{Codec: "aac", Channels: 6, Default: "True"}); got != "AAC 6ch (默认)" {
		t.Errorf("audioDisplayTitle = %q", got)
	}
	// und 不应作为展示语言外露
	if got := displayLanguage("und"); got != "" {
		t.Errorf("displayLanguage(und) = %q, want 空", got)
	}
	if got := displayLanguage("jpn"); got != "Japanese" {
		t.Errorf("displayLanguage(jpn) = %q", got)
	}
}

// TestItemFieldExposure 覆盖「入库时间 / 媒体路径 / 媒体信息」在四个接口的返回位置，
// 语义与真实 Emby 4.9 实测一致：
//   - 列表/Latest：DateCreated/Path/MediaSources 均需 ?Fields= 显式索取
//   - 详情：恒返回全部（含 DateModified/FileName/顶层 MediaStreams/Width/Height）
//   - PlaybackInfo：只返回 MediaSources + PlaySessionId
//
// 同时验证 Fields=MediaSources 时条目额外多出的顶层 Bitrate/Container 便捷字段。
func TestItemFieldExposure(t *testing.T) {
	if _, err := probe.LookPath(""); err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	media := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	defer media.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.strm"), media.URL+"/probe-sample.mp4\n")
	// NFO 带流信息，使 MediaStreams 有真实内容可校验。
	writeFile(t, filepath.Join(root, "x.nfo"),
		`<movie><title>X</title><fileinfo><streamdetails>`+
			`<video><codec>h264</codec><width>1920</width><height>1080</height><bitrate>5000000</bitrate>`+
			`<profile>High</profile><bitdepth>8</bitdepth><reframes>1</reframes>`+
			`<colortransfer>smpte2084</colortransfer><colorprimaries>bt2020</colorprimaries>`+
			`</video>`+
			`<audio><codec>aac</codec><channels>2</channels><samplingrate>48000</samplingrate>`+
			`<channellayout>stereo</channellayout><bitrate>192000</bitrate></audio>`+
			`</streamdetails></fileinfo></movie>`)

	a, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	call := func(method, path, token string) (int, any) {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var v any
		_ = json.Unmarshal(raw, &v)
		return resp.StatusCode, v
	}

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
	if token == "" {
		t.Fatal("未取得令牌")
	}

	// Windows 路径含反斜杠，JSON 里需转义。
	libBody := `{"Name":"L","Path":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	if resp, body := post("/api/admin/libraries", libBody, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("建库失败: %d %s", resp.StatusCode, body)
	}
	if resp, body := post("/api/admin/scan", "", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("扫描失败: %d %s", resp.StatusCode, body)
	}

	id, err := a.db.MovieIDByPath(filepath.Join(root, "x.strm"))
	if err != nil || id == 0 {
		t.Fatalf("取索引 id 失败: %d %v", id, err)
	}
	ids := strconv.FormatInt(id, 10)

	itemOf := func(body any) map[string]any {
		v, _ := body.(map[string]any)
		items, _ := v["Items"].([]any)
		if len(items) == 0 {
			t.Fatalf("列表为空: %v", body)
		}
		first, _ := items[0].(map[string]any)
		return first
	}

	// —— 列表：默认不含三项 ——
	_, plain := call("GET", "/Users/1/Items", token)
	item := itemOf(plain)
	for _, key := range []string{"DateCreated", "Path", "MediaSources"} {
		if _, exists := item[key]; exists {
			t.Errorf("列表默认不应含 %s", key)
		}
	}

	// —— 列表：Fields=MediaSources,DateCreated,Path ——
	_, withFields := call("GET", "/Users/1/Items?Fields=MediaSources,DateCreated,Path", token)
	item = itemOf(withFields)
	if item["Path"] != filepath.Join(root, "x.strm") {
		t.Errorf("Path = %v, want .strm 路径", item["Path"])
	}
	created, _ := item["DateCreated"].(string)
	if !strings.HasSuffix(created, "Z") || !strings.Contains(created, "T") {
		t.Errorf("DateCreated 格式不符 Emby 契约: %q", created)
	}
	if _, exists := item["DateModified"]; !exists {
		t.Error("Fields=DateCreated 应同时返回 DateModified")
	}
	// Fields=MediaSources 时条目额外多出顶层 Bitrate/Container
	if item["Container"] != "mp4" {
		t.Errorf("顶层 Container = %v, want mp4", item["Container"])
	}
	if got, ok := item["Bitrate"].(float64); !ok || got != 5000000+192000 {
		t.Errorf("顶层 Bitrate = %v, want %v", item["Bitrate"], 5000000+192000)
	}

	// —— 详情：无需 Fields 即返回全部 ——
	_, detailBody := call("GET", "/Users/1/Items/"+ids, token)
	detail, _ := detailBody.(map[string]any)
	for _, key := range []string{"DateCreated", "DateModified", "Path", "FileName", "MediaSources", "MediaStreams", "Width", "Height", "PartCount", "Bitrate", "Container"} {
		if _, exists := detail[key]; !exists {
			t.Errorf("详情缺少 %s", key)
		}
	}
	if detail["FileName"] != "x.strm" {
		t.Errorf("FileName = %v, want x.strm", detail["FileName"])
	}
	if detail["Width"].(float64) != 1920 || detail["Height"].(float64) != 1080 {
		t.Errorf("详情 Width/Height = %v/%v", detail["Width"], detail["Height"])
	}
	// 顶层 MediaStreams 应与首个媒体源的 MediaStreams 一致
	topStreams, _ := detail["MediaStreams"].([]any)
	src, _ := detail["MediaSources"].([]any)[0].(map[string]any)
	innerStreams, _ := src["MediaStreams"].([]any)
	if len(topStreams) == 0 || len(topStreams) != len(innerStreams) {
		t.Errorf("顶层 MediaStreams(%d) 与媒体源(%d) 不一致", len(topStreams), len(innerStreams))
	}
	// 展示标题按真实 Emby 规则：2160p 档位 + VideoRange + Codec
	var videoStream map[string]any
	for _, raw := range topStreams {
		if s, _ := raw.(map[string]any); s["Type"] == "Video" {
			videoStream = s
			break
		}
	}
	if videoStream == nil {
		t.Fatal("详情 MediaStreams 里没有视频轨")
	}
	if videoStream["VideoRange"] != "HDR 10" {
		t.Errorf("VideoRange = %v, want HDR 10", videoStream["VideoRange"])
	}
	if videoStream["DisplayTitle"] != "1080p HDR 10 H264" {
		t.Errorf("DisplayTitle = %v, want 1080p HDR 10 H264", videoStream["DisplayTitle"])
	}

	// —— PlaybackInfo：只返回 MediaSources + PlaySessionId ——
	_, pbBody := call("GET", "/Items/"+ids+"/PlaybackInfo", token)
	pb, _ := pbBody.(map[string]any)
	if _, exists := pb["MediaSources"]; !exists {
		t.Error("PlaybackInfo 缺少 MediaSources")
	}
	if _, exists := pb["PlaySessionId"]; !exists {
		t.Error("PlaybackInfo 缺少 PlaySessionId")
	}
	if _, exists := pb["DateCreated"]; exists {
		t.Error("PlaybackInfo 不应含 DateCreated")
	}

	// —— Latest：默认不含，Fields 后含 ——
	_, latestPlain := call("GET", "/Users/1/Items/Latest", token)
	if list, ok := latestPlain.([]any); ok && len(list) > 0 {
		first, _ := list[0].(map[string]any)
		if _, exists := first["DateCreated"]; exists {
			t.Error("Latest 默认不应含 DateCreated")
		}
	} else {
		t.Errorf("Latest 应返回裸数组: %T", latestPlain)
	}
	_, latestFields := call("GET", "/Users/1/Items/Latest?Fields=DateCreated,MediaSources", token)
	if list, ok := latestFields.([]any); ok && len(list) > 0 {
		first, _ := list[0].(map[string]any)
		for _, key := range []string{"DateCreated", "MediaSources"} {
			if _, exists := first[key]; !exists {
				t.Errorf("Latest Fields 后缺少 %s", key)
			}
		}
	}
}
