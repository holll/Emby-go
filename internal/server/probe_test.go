package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/nfo"
	"emby-go/internal/probe"
	"emby-go/internal/store"
)

// TestMediaProbe 端到端覆盖媒体信息探测：真实调用 ffprobe 探测本地 HTTP 提供的样片，
// 校验「写回 NFO」「跳过已探测项」「无 NFO 拒绝」「字段透出到 Emby API」四条链路。
// 环境无 ffprobe 时跳过（该功能依赖系统 ffprobe，CI 缺依赖不应判失败）。
func TestMediaProbe(t *testing.T) {
	if _, err := probe.LookPath(""); err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	// 用本地 HTTP 提供样片，模拟 .strm 指向的远程直链。
	media := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	defer media.Close()
	sampleURL := media.URL + "/probe-sample.mp4"

	root := t.TempDir()
	// a：有 NFO 但无流信息 → 应被探测并写入；NFO 里的未建模元素必须保留。
	writeFile(t, filepath.Join(root, "a.strm"), sampleURL+"\n")
	writeFile(t, filepath.Join(root, "a.nfo"),
		"<movie>\n  <title>Alpha</title>\n  <customtag>keepme</customtag>\n</movie>")
	// b：已有 streamdetails → only_missing 时应跳过。
	writeFile(t, filepath.Join(root, "b.strm"), sampleURL+"\n")
	writeFile(t, filepath.Join(root, "b.nfo"),
		"<movie><title>Beta</title><fileinfo><streamdetails><video><codec>h264</codec><width>1920</width><height>1080</height></video></streamdetails></fileinfo></movie>")
	// c：无 NFO（待补录）→ 不应进入探测候选。
	writeFile(t, filepath.Join(root, "c.strm"), sampleURL+"\n")

	a, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	call := func(method, path, token, body string) (*http.Response, map[string]any) {
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
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		return resp, v
	}

	if resp, _ := call("POST", "/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d", resp.StatusCode)
	}
	_, auth := call("POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`)
	token, _ := auth["AccessToken"].(string)
	if token == "" {
		t.Fatalf("登录失败: %v", auth)
	}
	libBody := `{"Name":"AV","Path":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	if resp, _ := call("POST", "/api/admin/libraries", token, libBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("add library: %d", resp.StatusCode)
	}
	if resp, scan := call("POST", "/api/admin/scan", token, ""); resp.StatusCode != http.StatusOK || scan["success"].(float64) != 2 {
		t.Fatalf("scan: status=%d body=%v", resp.StatusCode, scan)
	}

	// 直接按磁盘路径取条目 id：a 缺流信息、b 已带 streamdetails、c 是待补录。
	// 不走 /api/admin/items——它默认只返回 success/manual，看不到待补录的 c。
	ids := map[string]int64{}
	for _, name := range []string{"a", "b", "c"} {
		id, err := a.db.MovieIDByPath(filepath.Join(root, name+".strm"))
		if err != nil || id == 0 {
			t.Fatalf("取 %s 的索引 id 失败: id=%d err=%v", name, id, err)
		}
		ids[name] = id
	}

	// —— 全库探测：a（无流信息）与 b（刮削器写的不完整 streamdetails）都该被探测 ——
	// 关键点：b 虽有 <streamdetails> 但没有我们的探测版本标记，必须被补齐而不是跳过。
	resp, started := call("POST", "/api/admin/probe/media", token, `{"only_missing":true}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("启动探测: status=%d body=%v", resp.StatusCode, started)
	}
	if total := started["total"].(float64); total != 2 {
		t.Fatalf("候选数 = %v, want 2（a、b；c 无 NFO 应排除）", total)
	}
	progress := waitProbeDone(t, call, token)
	if progress["success"].(float64) != 2 || progress["skipped"].(float64) != 0 || progress["failed"].(float64) != 0 {
		t.Fatalf("探测结果 = %v（期望 成功2 跳过0 失败0：刮削器写的不完整数据也要补齐）", progress)
	}

	// —— 写入 NFO 的内容必须准确，且原有未建模元素不丢 ——
	nfoPath := filepath.Join(root, "a.nfo")
	raw, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"<title>Alpha</title>", "<customtag>keepme</customtag>", // 原内容保留
		"<codec>h264</codec>", "<width>320</width>", "<height>240</height>", // 探测所得
		"<aspectratio>4:3</aspectratio>", "<profile>High</profile>", "<level>30</level>",
		"<pixelformat>yuv420p</pixelformat>", "<bitdepth>8</bitdepth>", "<reframes>1</reframes>",
		"<colortransfer>bt709</colortransfer>", "<colorprimaries>bt709</colorprimaries>", "<colorspace>bt709</colorspace>",
		"<scantype>progressive</scantype>", "<framerate>10.00000</framerate>",
		"<codec>aac</codec>", "<channels>2</channels>", "<samplingrate>44100</samplingrate>",
		"<channellayout>stereo</channellayout>",
		"<probeversion>1</probeversion>", // 幂等标记：下次全库探测据此跳过
	} {
		if !strings.Contains(text, want) {
			t.Errorf("NFO 缺少 %q\n实际内容:\n%s", want, text)
		}
	}
	if strings.Count(text, "<fileinfo>") != 1 {
		t.Errorf("fileinfo 块数 = %d, want 1", strings.Count(text, "<fileinfo>"))
	}
	// b 的旧 streamdetails（1920x1080）应被探测结果覆盖
	rawB, _ := os.ReadFile(filepath.Join(root, "b.nfo"))
	if strings.Contains(string(rawB), "<width>1920</width>") {
		t.Error("刮削器写入的旧流信息未被探测结果覆盖")
	}

	// —— 幂等性：紧接着再跑一次全库探测，两条都应跳过且不发 ffprobe 请求 ——
	// 这是「全库全量扫描」的核心保证：重复触发代价仅为读一遍 NFO。
	if resp, again := call("POST", "/api/admin/probe/media", token, `{"only_missing":true}`); resp.StatusCode != http.StatusAccepted || again["total"].(float64) != 2 {
		t.Fatalf("二次探测启动失败: status=%d body=%v", resp.StatusCode, again)
	}
	progress = waitProbeDone(t, call, token)
	if progress["success"].(float64) != 0 || progress["skipped"].(float64) != 2 || progress["failed"].(float64) != 0 {
		t.Fatalf("二次探测应为全跳过: %v（期望 成功0 跳过2 失败0）", progress)
	}
	// 跳过的条目 NFO 不得被重写（内容应逐字不变）
	rawAfter, _ := os.ReadFile(nfoPath)
	if string(rawAfter) != string(raw) {
		t.Error("跳过条目的 NFO 被改动了")
	}

	// —— only_missing=false 显式强制：即使已是最新版本也重探 ——
	if resp, forced := call("POST", "/api/admin/probe/media", token, `{"only_missing":false}`); resp.StatusCode != http.StatusAccepted || forced["total"].(float64) != 2 {
		t.Fatalf("强制探测启动失败: status=%d body=%v", resp.StatusCode, forced)
	}
	progress = waitProbeDone(t, call, token)
	if progress["success"].(float64) != 2 || progress["skipped"].(float64) != 0 {
		t.Fatalf("强制探测应全部重探: %v（期望 成功2 跳过0）", progress)
	}

	// —— 探测结果应立刻透出到 Emby API（MediaStreams / DisplayTitle / Bitrate） ——
	resp, playback := call("GET", "/Items/"+strconv.FormatInt(ids["a"], 10)+"/PlaybackInfo", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PlaybackInfo: %d", resp.StatusCode)
	}
	source := playback["MediaSources"].([]any)[0].(map[string]any)
	streams := source["MediaStreams"].([]any)
	if len(streams) != 2 {
		t.Fatalf("MediaStreams 长度 = %d, want 2", len(streams))
	}
	video := streams[0].(map[string]any)
	// 展示格式与真实 Emby 一致：分辨率档位 + 编码（不含 Profile）。
	if title, _ := video["DisplayTitle"].(string); title != "240p H264" {
		t.Errorf("DisplayTitle = %q, want 240p H264", video["DisplayTitle"])
	}
	if video["Width"].(float64) != 320 || video["Height"].(float64) != 240 {
		t.Errorf("分辨率未透出: %v", video)
	}
	if video["BitRate"].(float64) != 74160 {
		t.Errorf("视频码率 = %v, want 74160", video["BitRate"])
	}
	if video["IsInterlaced"] != false {
		t.Errorf("IsInterlaced = %v, want false", video["IsInterlaced"])
	}
	if video["PixelFormat"] != "yuv420p" || video["Profile"] != "High" {
		t.Errorf("PixelFormat/Profile = %v/%v", video["PixelFormat"], video["Profile"])
	}
	// 样片带 BT.709 色彩元数据 → 应判 SDR（依据传输特性，不是猜位深）。
	if video["VideoRange"] != "SDR" {
		t.Errorf("VideoRange = %v, want SDR", video["VideoRange"])
	}
	// 真实 Emby 对每个轨恒返回这些字段，客户端会无条件读取。
	if video["IsHearingImpaired"] != false || video["IsAnamorphic"] != false || video["IsExternal"] != false {
		t.Errorf("布尔字段 = %v/%v/%v", video["IsHearingImpaired"], video["IsAnamorphic"], video["IsExternal"])
	}
	if video["Protocol"] != "Http" || video["ExtendedVideoType"] != "None" {
		t.Errorf("Protocol/ExtendedVideoType = %v/%v", video["Protocol"], video["ExtendedVideoType"])
	}
	if _, exists := video["DisplayLanguage"]; exists {
		t.Error("und 语言不应输出 DisplayLanguage")
	}
	audio := streams[1].(map[string]any)
	if audio["ChannelLayout"] != "stereo" || audio["Channels"].(float64) != 2 {
		t.Errorf("音频轨 = %v", audio)
	}
	// 容器总码率 = 视频 + 音频
	if got := source["Bitrate"].(float64); got != 74160+45212 {
		t.Errorf("容器总码率 = %v, want %v", got, 74160+45212)
	}

	// —— 单条探测：b 已有流信息也应被覆盖为探测结果 ——
	resp, single := call("POST", "/api/admin/items/"+strconv.FormatInt(ids["b"], 10)+"/probe", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("单条探测: status=%d body=%v", resp.StatusCode, single)
	}
	info := single["info"].(map[string]any)
	if infoVideo := info["video"].(map[string]any); infoVideo["width"].(float64) != 320 {
		t.Errorf("单条探测未返回视频信息: %v", info)
	}
	rawB, _ = os.ReadFile(filepath.Join(root, "b.nfo"))
	if strings.Contains(string(rawB), "<width>1920</width>") {
		t.Error("单条探测应覆盖旧 streamdetails")
	}
	if !strings.Contains(string(rawB), "<width>320</width>") {
		t.Errorf("单条探测未写入新值:\n%s", rawB)
	}

	// —— 待补录条目（无 NFO）应被拒绝，避免凭空造 NFO 破坏状态判定 ——
	if resp, body := call("POST", "/api/admin/items/"+strconv.FormatInt(ids["c"], 10)+"/probe", token, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("无 NFO 应 409: status=%d body=%v", resp.StatusCode, body)
	}

	// —— 任务记录应留痕，供「任务」页查看 ——
	if _, tasks := call("GET", "/api/admin/tasks", token, ""); !strings.Contains(mustJSON(t, tasks), `"probe"`) {
		t.Error("应记录 type=probe 的任务")
	}
}

// waitProbeDone 轮询探测进度直到结束，超时即判失败（避免异步任务把测试挂死）。
func waitProbeDone(t *testing.T, call func(string, string, string, string) (*http.Response, map[string]any), token string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_, progress := call("GET", "/api/admin/probe/media/progress", token, "")
		if running, _ := progress["running"].(bool); !running && progress["finished_at"] != nil && progress["finished_at"] != "" {
			return progress
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("探测任务未在 60s 内结束")
	return nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestNfoProbeVersion 覆盖「是否已探测」的判定。这是全库探测幂等性的核心：
// 判据必须是我们的版本标记，不能是「有无 <streamdetails>」——
// 刮削器同样会写 streamdetails（且常不完整），按有无判定会让该补齐的条目被永久跳过。
func TestNfoProbeVersion(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"无 fileinfo（未探测）", `<movie><title>A</title></movie>`, 0},
		{"刮削器的部分 streamdetails（无版本标记）",
			`<movie><title>B</title><fileinfo><streamdetails><video><codec>h264</codec></video></streamdetails></fileinfo></movie>`, 0},
		{"已按 v1 探测", `<movie><title>C</title><fileinfo><size>123</size><probeversion>1</probeversion><streamdetails><video><codec>h264</codec></video></streamdetails></fileinfo></movie>`, 1},
		{"更高版本", `<movie><title>D</title><fileinfo><probeversion>2</probeversion><streamdetails><video><codec>h264</codec></video></streamdetails></fileinfo></movie>`, 2},
		{"空 streamdetails 也按版本判（不是有内容就算）",
			`<movie><title>E</title><fileinfo><streamdetails></streamdetails></fileinfo></movie>`, 0},
	}
	for _, tc := range cases {
		path := filepath.Join(root, tc.name+".nfo")
		if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
			t.Fatal(err)
		}
		if got := nfoProbeState(path).Version; got != tc.want {
			t.Errorf("%s: nfoProbeVersion = %d, want %d", tc.name, got, tc.want)
		}
	}
	// 文件不存在 / 路径为空 / XML 损坏 → 均视为未探测（应尝试探测而非跳过）
	if got := nfoProbeState(filepath.Join(root, "missing.nfo")).Version; got != 0 {
		t.Errorf("缺失文件 = %d, want 0", got)
	}
	if got := nfoProbeState("").Version; got != 0 {
		t.Errorf("空路径 = %d, want 0", got)
	}
	broken := filepath.Join(root, "broken.nfo")
	os.WriteFile(broken, []byte("<movie><title>unclosed"), 0644)
	if got := nfoProbeState(broken).Version; got != 0 {
		t.Errorf("损坏 NFO = %d, want 0", got)
	}

	// 版本比较语义：>= probeVersion 才跳过，因此旧版本（0）必须被重探
	old := filepath.Join(root, "old.nfo")
	os.WriteFile(old, []byte(`<movie><fileinfo><probeversion>0</probeversion></fileinfo></movie>`), 0644)
	if got := nfoProbeState(old).Version; got >= probeVersion {
		t.Errorf("版本 0 应低于 probeVersion(%d) 而触发重探，实际 %d", probeVersion, got)
	}
}

// TestProbeStateIsCurrent 覆盖「该探测结果对当前直链是否仍然有效」的判定。
// 关键场景是换源：.strm 改指向别的文件后，旧的分辨率/码率已不适用，必须重探——
// 只认版本号会一直沿用错误参数。
func TestProbeStateIsCurrent(t *testing.T) {
	const url = "https://cdn.example/film.mp4"
	cases := []struct {
		name  string
		state probeState
		want  bool
	}{
		{"版本当前且直链一致", probeState{Version: probeVersion, URL: url}, true},
		{"版本偏低", probeState{Version: probeVersion - 1, URL: url}, false},
		{"从未探测", probeState{}, false},
		{"换源（直链不同）", probeState{Version: probeVersion, URL: "https://cdn.example/other.mp4"}, false},
		// 旧版本写入的条目没有 <probeurl>：只认版本号，避免升级后全库重探
		{"无直链记录（旧条目）", probeState{Version: probeVersion}, true},
	}
	for _, tc := range cases {
		if got := tc.state.isCurrent(url); got != tc.want {
			t.Errorf("%s: isCurrent = %v, want %v", tc.name, got, tc.want)
		}
	}
	// 探测写入后，NFO 里的直链必须可回读
	path := filepath.Join(t.TempDir(), "u.nfo")
	writeFile(t, path, "<movie><title>U</title></movie>")
	if err := nfo.SaveFileInfo(path, nfo.FileInfoMeta{Size: 1, ProbeVersion: probeVersion, ProbeURL: url}, sampleStreamDetails()); err != nil {
		t.Fatal(err)
	}
	state := nfoProbeState(path)
	if state.URL != url || !state.isCurrent(url) {
		t.Errorf("回读状态 = %+v, want 直链 %q 且有效", state, url)
	}
	if state.isCurrent("https://cdn.example/changed.mp4") {
		t.Error("换源后应判定为失效")
	}
}

// TestProbeFailureRetries 覆盖失败不留标记：探测失败时 NFO 不被改动，
// 于是下次全库探测仍会把它当未探测处理，从而自动重试。
func TestProbeFailureRetries(t *testing.T) {
	if _, err := probe.LookPath(""); err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	root := t.TempDir()
	// 指向一个必然失败的目标（未监听的端口）
	writeFile(t, filepath.Join(root, "bad.strm"), "http://127.0.0.1:1/nope.mp4\n")
	original := `<movie><title>Bad</title></movie>`
	writeFile(t, filepath.Join(root, "bad.nfo"), original)

	a, err := newApp(config.Config{DBPath: filepath.Join(root, "t.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ffprobe, _ := probe.LookPath("")

	movie := store.Movie{ID: 1, LibraryID: 1, NFOPath: filepath.Join(root, "bad.nfo"), SourcePath: filepath.Join(root, "bad.strm")}
	touched := make(chan int64, 1)
	a.beginProbe(1)
	a.probeOne(a.probeCtx, ffprobe, probeTarget{Movie: movie, Path: movie.SourcePath, Primary: true, Index: 1}, true, touched)

	a.probeTaskMu.RLock()
	failed := a.probeStatus.Failed
	a.probeTaskMu.RUnlock()
	if failed != 1 {
		t.Fatalf("失败计数 = %d, want 1", failed)
	}
	// 失败不得改动 NFO，否则会被误判为已探测而永不重试
	raw, _ := os.ReadFile(movie.NFOPath)
	if string(raw) != original {
		t.Errorf("探测失败不应改动 NFO:\n%s", raw)
	}
	if got := nfoProbeState(movie.NFOPath).Version; got >= probeVersion {
		t.Errorf("失败条目不应留下已探测标记，实际版本 %d", got)
	}
}

// sampleStreamDetails 供探测状态相关测试复用的最小流信息。
func sampleStreamDetails() *nfo.StreamDetails {
	return &nfo.StreamDetails{Video: &nfo.VideoStream{Codec: "h264", Width: 1920, Height: 1080}}
}
