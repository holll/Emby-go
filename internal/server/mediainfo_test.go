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
	"sync/atomic"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/probe"
	"emby-go/internal/store"
)

// countingMedia 包装文件服务并统计请求数。
// 探测必然要访问媒体源，因此请求数就是「是否真的发起了 ffprobe」的直接证据。
func countingMedia(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	files := http.FileServer(http.Dir("testdata"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// newProbeTestApp 建一个已登录、已建库、已扫描的测试实例。
func newProbeTestApp(t *testing.T, root string) (*App, *httptest.Server, string) {
	t.Helper()
	app, err := newApp(config.Config{DBPath: filepath.Join(root, "t.db")}, cache.NewMemory(256))
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
	if token == "" {
		t.Fatal("未取得令牌")
	}
	libBody := `{"Name":"L","Path":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	if resp, body := post("/api/admin/libraries", libBody, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("建库: %d %s", resp.StatusCode, body)
	}
	if resp, body := post("/api/admin/scan", "", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("扫描: %d %s", resp.StatusCode, body)
	}
	return app, ts, token
}

// TestMediaInfoReuse 用「媒体源实际收到的请求数」证明复用生效：
// 重复的全库探测必须是零请求，缺 NFO 时用本地原始数据回填也必须零请求。
func TestMediaInfoReuse(t *testing.T) {
	if _, err := probe.LookPath(""); err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	media, hits := countingMedia(t)
	root := t.TempDir()
	strm := filepath.Join(root, "film.strm")
	nfoPath := filepath.Join(root, "film.nfo")
	writeFile(t, strm, media.URL+"/probe-sample.mp4\n")
	writeFile(t, nfoPath, "<movie>\n  <title>Film</title>\n</movie>")

	_, ts, token := newProbeTestApp(t, root)

	probeAll := func(onlyMissing bool) map[string]any {
		body := `{"only_missing":true}`
		if !onlyMissing {
			body = `{"only_missing":false}`
		}
		req, _ := http.NewRequest("POST", ts.URL+"/api/admin/probe/media", strings.NewReader(body))
		req.Header.Set("X-Emby-Token", token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("启动探测: %d", resp.StatusCode)
		}
		return waitProbeDone(t, func(method, path, tok, body string) (*http.Response, map[string]any) {
			return embyCall(t, ts, method, path, tok, body)
		}, token)
	}

	// —— 首次全库探测：产生 1 次源请求，落盘 mediainfo.json ——
	p := probeAll(true)
	if p["success"].(float64) != 1 {
		t.Fatalf("首次探测 = %v, want 成功1", p)
	}
	afterFirst := atomic.LoadInt64(hits)
	if afterFirst == 0 {
		t.Fatal("首次探测未访问媒体源？")
	}

	sidecar := filepath.Join(root, "mediainfo.json")
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("mediainfo.json 未生成: %v", err)
	}
	var file mediaInfoFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("mediainfo.json 解析失败: %v", err)
	}
	if file.ProbeVersion != probeVersion {
		t.Errorf("probe_version = %d, want %d", file.ProbeVersion, probeVersion)
	}
	if file.Source != media.URL+"/probe-sample.mp4" {
		t.Errorf("source = %q", file.Source)
	}
	if file.MediaFile != "probe-sample.mp4" {
		t.Errorf("media_file = %q", file.MediaFile)
	}
	// 原始输出必须完整保留（这是将来新增字段免重探的原料）
	if !strings.Contains(string(file.FFProbe), "\"codec_name\"") || !strings.Contains(string(file.FFProbe), "\"format\"") {
		t.Errorf("ffprobe 原始输出不完整: %s", file.FFProbe)
	}

	// —— 二次全库探测：必须零请求、全部跳过 ——
	p = probeAll(true)
	if p["success"].(float64) != 0 || p["skipped"].(float64) != 1 {
		t.Fatalf("二次探测 = %v, want 跳过1", p)
	}
	if got := atomic.LoadInt64(hits); got != afterFirst {
		t.Errorf("二次探测访问了媒体源 %d 次（应为 0，即零 ffprobe）", got-afterFirst)
	}

	// —— 三次：NFO 数据丢失（外部改动），有本地缓存 → 零请求回填 ——
	writeFile(t, nfoPath, "<movie>\n  <title>Film</title>\n  <customtag>keep</customtag>\n</movie>")
	p = probeAll(true)
	if p["success"].(float64) != 1 || p["failed"].(float64) != 0 {
		t.Fatalf("回填 = %v, want 成功1 失败0", p)
	}
	if got := atomic.LoadInt64(hits); got != afterFirst {
		t.Errorf("回填访问了媒体源 %d 次（应为 0：本地原始数据足够）", got-afterFirst)
	}
	backfilled, _ := os.ReadFile(nfoPath)
	for _, want := range []string{"<width>320</width>", "<size>9260</size>", "<probeversion>1</probeversion>", "<customtag>keep</customtag>"} {
		if !strings.Contains(string(backfilled), want) {
			t.Errorf("回填后的 NFO 缺少 %q:\n%s", want, backfilled)
		}
	}

	// —— 四次：缓存版本偏低（模拟探测逻辑升级）→ 仍零请求，本地重新映射 ——
	file.ProbeVersion = 0
	downgraded, _ := json.Marshal(file)
	if err := os.WriteFile(sidecar, downgraded, 0644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nfoPath, "<movie>\n  <title>Film</title>\n</movie>")
	p = probeAll(true)
	if p["success"].(float64) != 1 || p["failed"].(float64) != 0 {
		t.Fatalf("版本升级回填 = %v, want 成功1 失败0", p)
	}
	if got := atomic.LoadInt64(hits); got != afterFirst {
		t.Errorf("版本升级访问了媒体源 %d 次（应为 0：本地即可升级）", got-afterFirst)
	}
	var upgraded mediaInfoFile
	rawUpgraded, _ := os.ReadFile(sidecar)
	json.Unmarshal(rawUpgraded, &upgraded)
	if upgraded.ProbeVersion != probeVersion {
		t.Errorf("回填后缓存版本 = %d, want %d", upgraded.ProbeVersion, probeVersion)
	}

	// —— 强制探测：语义是「现在重新探」，必须真的访问源并刷新缓存 ——
	p = probeAll(false)
	if p["success"].(float64) != 1 {
		t.Fatalf("强制探测 = %v, want 成功1", p)
	}
	if got := atomic.LoadInt64(hits); got <= afterFirst {
		t.Error("强制探测应真实访问媒体源")
	}

	// —— 源更换（.strm 指向别的文件）→ 缓存失效，必须重探 ——
	before := atomic.LoadInt64(hits)
	writeFile(t, strm, media.URL+"/probe-sample.mp4?changed=1\n")
	p = probeAll(true)
	if p["success"].(float64) != 1 {
		t.Fatalf("源更换后 = %v, want 成功1（旧缓存必须失效）", p)
	}
	if got := atomic.LoadInt64(hits); got <= before {
		t.Error("源更换后应重新探测")
	}
}

// TestMediaInfoPathLayout 覆盖 mediainfo.json 的落位规则：
// 一片一目录用固定的 mediainfo.json；同目录多 .strm 时各自用 <basename>.mediainfo.json 避免互相覆盖。
func TestMediaInfoPathLayout(t *testing.T) {
	root := t.TempDir()
	single := filepath.Join(root, "one", "film.strm")
	os.MkdirAll(filepath.Dir(single), 0755)
	os.WriteFile(single, []byte("http://x/y.mp4\n"), 0644)
	if got := mediaInfoPath(single); filepath.Base(got) != "mediainfo.json" {
		t.Errorf("单片目录应使用 mediainfo.json，实际 %q", got)
	}

	multi := filepath.Join(root, "multi")
	os.MkdirAll(multi, 0755)
	cd1 := filepath.Join(multi, "film-CD1.strm")
	cd2 := filepath.Join(multi, "film-CD2.strm")
	os.WriteFile(cd1, []byte("http://x/1.mp4\n"), 0644)
	os.WriteFile(cd2, []byte("http://x/2.mp4\n"), 0644)
	p1, p2 := mediaInfoPath(cd1), mediaInfoPath(cd2)
	if p1 == p2 {
		t.Fatalf("同目录多 .strm 时缓存路径不得相同：%q", p1)
	}
	if filepath.Base(p1) != "film-CD1.mediainfo.json" || filepath.Base(p2) != "film-CD2.mediainfo.json" {
		t.Errorf("多片目录应各自命名，实际 %q / %q", p1, p2)
	}
}

// TestLoadMediaInfoValidation 覆盖缓存失效的几种情形，任一命中都应重探而非误用旧数据。
func TestLoadMediaInfoValidation(t *testing.T) {
	root := t.TempDir()
	strm := filepath.Join(root, "f.strm")
	writeFile(t, strm, "http://x/f.mp4\n")
	path := mediaInfoPath(strm)
	source := "http://x/f.mp4"

	// 不存在
	if _, ok := loadMediaInfo(strm, source); ok {
		t.Error("缓存不存在时应返回 false")
	}
	// 损坏 JSON
	os.WriteFile(path, []byte("{not json"), 0644)
	if _, ok := loadMediaInfo(strm, source); ok {
		t.Error("缓存损坏时应返回 false")
	}
	// ffprobe 段缺失
	os.WriteFile(path, []byte(`{"probe_version":1,"source":"http://x/f.mp4"}`), 0644)
	if _, ok := loadMediaInfo(strm, source); ok {
		t.Error("ffprobe 段为空时应返回 false")
	}
	// 源不一致（.strm 已指向别的文件）→ 旧参数不适用
	valid, _ := json.Marshal(mediaInfoFile{ProbeVersion: 1, Source: "http://x/other.mp4", FFProbe: json.RawMessage(`{"streams":[1]}`)})
	os.WriteFile(path, valid, 0644)
	if _, ok := loadMediaInfo(strm, source); ok {
		t.Error("源变化时应返回 false（必须重探）")
	}
	// 正常
	valid, _ = json.Marshal(mediaInfoFile{ProbeVersion: 1, Source: source, FFProbe: json.RawMessage(`{"streams":[1]}`)})
	os.WriteFile(path, valid, 0644)
	file, ok := loadMediaInfo(strm, source)
	if !ok || file.ProbeVersion != 1 {
		t.Errorf("有效缓存应读取成功: ok=%v file=%+v", ok, file)
	}
}

// TestSaveMediaInfoAtomic 验证写入的 JSON 结构完整且不留临时文件。
func TestSaveMediaInfoAtomic(t *testing.T) {
	root := t.TempDir()
	strm := filepath.Join(root, "f.strm")
	writeFile(t, strm, "http://x/f.mp4\n")
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"duration":"12.5","size":"12345"}}`)
	if err := saveMediaInfo(strm, "http://x/f.mp4", raw); err != nil {
		t.Fatalf("saveMediaInfo: %v", err)
	}
	path := mediaInfoPath(strm)
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("不应残留 .tmp")
	}
	file, ok := loadMediaInfo(strm, "http://x/f.mp4")
	if !ok {
		t.Fatal("写入后应可读回")
	}
	info, err := infoFromMediaInfo(file)
	if err != nil {
		t.Fatalf("原始数据应可重新解析: %v", err)
	}
	if info.Video == nil || info.Video.Width != 1920 || info.DurationSeconds != 12 {
		t.Errorf("重新解析结果不符: %+v", info)
	}
	// 写入的应是缩进后的可读 JSON
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "\n  \"probe_version\"") {
		t.Errorf("应输出缩进 JSON:\n%s", content)
	}
}

// embyCall 供 waitProbeDone 复用的请求助手。
func embyCall(t *testing.T, ts *httptest.Server, method, path, token, body string) (*http.Response, map[string]any) {
	t.Helper()
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

// TestMultiPartProbe 覆盖分集影片：CD1/CD2/CD3 各有自己的 .strm，
// 每个都必须被探测并各自留下 mediainfo.json（探测单位是文件，不是影片）。
func TestMultiPartProbe(t *testing.T) {
	if _, err := probe.LookPath(""); err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	media, hits := countingMedia(t)
	root := t.TempDir()
	target := media.URL + "/probe-sample.mp4"
	// CD 分组：主文件 + 两个分段，同一目录（这是 scanner 的 stacking 布局）。
	writeFile(t, filepath.Join(root, "系列-CD1.strm"), target+"\n")
	writeFile(t, filepath.Join(root, "系列-CD2.strm"), target+"\n")
	writeFile(t, filepath.Join(root, "系列-CD3.strm"), target+"\n")
	// CD1 与基准名共享一个 NFO（scanner 的 fallbackNFO 语义）。
	writeFile(t, filepath.Join(root, "系列.nfo"), "<movie>\n  <title>系列</title>\n</movie>\n")

	_, ts, token := newProbeTestApp(t, root)

	_, items := embyCall(t, ts, "GET", "/api/admin/items?limit=100", token, "")
	list, _ := items["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("应入库为 1 个逻辑影片（CD 分组），实际 %d", len(list))
	}
	first, _ := list[0].(map[string]any)
	if got := first["AdditionalParts"]; got == nil {
		t.Fatalf("未记录 AdditionalParts: %v", first)
	}
	parts, _ := first["AdditionalParts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("分段数 = %d, want 2", len(parts))
	}

	resp, started := embyCall(t, ts, "POST", "/api/admin/probe/media", token, `{"only_missing":true}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("启动探测: %d", resp.StatusCode)
	}
	// 1 个影片 → 3 个文件
	if total := started["total"].(float64); total != 3 {
		t.Fatalf("待探测文件数 = %v, want 3（主文件 + 2 分段）", total)
	}
	progress := waitProbeDone(t, func(method, path, tok, body string) (*http.Response, map[string]any) {
		return embyCall(t, ts, method, path, tok, body)
	}, token)
	if progress["success"].(float64) != 3 || progress["failed"].(float64) != 0 {
		t.Fatalf("探测结果 = %v, want 成功3", progress)
	}
	if atomic.LoadInt64(hits) == 0 {
		t.Fatal("分集探测未访问媒体源")
	}

	// 每个 .strm 都必须有自己的 mediainfo.json，且文件名带 basename 区分
	for _, name := range []string{"系列-CD1", "系列-CD2", "系列-CD3"} {
		path := filepath.Join(root, name+".mediainfo.json")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s 缺少 mediainfo.json: %v", name, err)
			continue
		}
		var file mediaInfoFile
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Errorf("%s 的 mediainfo.json 解析失败: %v", name, err)
			continue
		}
		if file.ProbeVersion != probeVersion || len(file.FFProbe) == 0 {
			t.Errorf("%s 缓存不完整: version=%d rawLen=%d", name, file.ProbeVersion, len(file.FFProbe))
		}
	}
	// 不应出现单一 mediainfo.json（多 .strm 目录下会互相覆盖）
	if _, err := os.Stat(filepath.Join(root, "mediainfo.json")); err == nil {
		t.Error("多分段目录不应使用单一 mediainfo.json（会互相覆盖）")
	}

	// 二次全库探测：3 个文件都应跳过且零联网
	before := atomic.LoadInt64(hits)
	embyCall(t, ts, "POST", "/api/admin/probe/media", token, `{"only_missing":true}`)
	progress = waitProbeDone(t, func(method, path, tok, body string) (*http.Response, map[string]any) {
		return embyCall(t, ts, method, path, tok, body)
	}, token)
	if progress["skipped"].(float64) != 3 || progress["success"].(float64) != 0 {
		t.Fatalf("二次探测 = %v, want 跳过3", progress)
	}
	if got := atomic.LoadInt64(hits); got != before {
		t.Errorf("二次探测访问了媒体源 %d 次（应为 0）", got-before)
	}

	// 单条探测（卡片按钮）：应一并刷新全部分段
	resp, single := embyCall(t, ts, "POST", "/api/admin/items/"+strconv.FormatInt(int64(first["id"].(float64)), 10)+"/probe", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("单条探测: %d %v", resp.StatusCode, single)
	}
	files, _ := single["files"].([]any)
	if len(files) != 3 {
		t.Fatalf("单条探测应覆盖 3 个文件，实际 %d", len(files))
	}
	kinds := make([]string, 0, 3)
	for _, raw := range files {
		f, _ := raw.(map[string]any)
		kinds = append(kinds, f["kind"].(string))
		if f["mediainfo_path"] == nil || f["info"] == nil {
			t.Errorf("文件条目缺少 mediainfo_path/info: %v", f)
		}
	}
	if kinds[0] != "primary" || kinds[1] != "part" || kinds[2] != "part" {
		t.Errorf("文件类型顺序 = %v, want [primary part part]", kinds)
	}
}

// TestProbeTargets 覆盖影片到文件的展开（探测单位是文件）。
func TestProbeTargets(t *testing.T) {
	movies := []store.Movie{
		{ID: 1, Title: "单片", SourcePath: "/lib/single.strm"},
		{ID: 2, Title: "分集", SourcePath: "/lib/mp-CD1.strm", AdditionalParts: []string{"/lib/mp-CD2.strm", "/lib/mp-CD3.strm"}},
	}
	targets := probeTargets(movies)
	if len(targets) != 4 {
		t.Fatalf("展开文件数 = %d, want 4（1 + 3）", len(targets))
	}
	first := targets[0]
	if !first.Primary || first.Index != 1 || first.Path != "/lib/single.strm" {
		t.Errorf("首个目标 = %+v", first)
	}
	// 分集：主文件 + 两个分段，序号 1/2/3
	if !targets[1].Primary || targets[1].Index != 1 || targets[1].Path != "/lib/mp-CD1.strm" {
		t.Errorf("分集主文件 = %+v", targets[1])
	}
	if targets[2].Primary || targets[2].Index != 2 || targets[2].Path != "/lib/mp-CD2.strm" {
		t.Errorf("CD2 = %+v", targets[2])
	}
	if targets[3].Index != 3 || targets[3].Path != "/lib/mp-CD3.strm" {
		t.Errorf("CD3 = %+v", targets[3])
	}
	// label 用于进度与失败信息
	if got := targets[1].label(); got != "分集" {
		t.Errorf("主文件 label = %q", got)
	}
	if got := targets[2].label(); got != "分集 - CD2" {
		t.Errorf("分段 label = %q", got)
	}
}

// TestMultiPartDistinctInfo 验证分段的媒体信息来自它自己的 mediainfo.json，
// 而不是沿用它读不到 NFO 时的主文件信息：
// CD1 指向 320x240、CD2 指向 640x360，探测后 CD2 的 PlaybackInfo 必须报 640x360。
func TestMultiPartDistinctInfo(t *testing.T) {
	if _, err := probe.LookPath(""); err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	media, _ := countingMedia(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "剧-CD1.strm"), media.URL+"/probe-sample.mp4\n")
	writeFile(t, filepath.Join(root, "剧-CD2.strm"), media.URL+"/probe-sample-hd.mp4\n")
	writeFile(t, filepath.Join(root, "剧.nfo"), "<movie>\n  <title>剧</title>\n</movie>\n")

	_, ts, token := newProbeTestApp(t, root)
	embyCall(t, ts, "POST", "/api/admin/probe/media", token, `{"only_missing":true}`)
	waitProbeDone(t, func(method, path, tok, body string) (*http.Response, map[string]any) {
		return embyCall(t, ts, method, path, tok, body)
	}, token)

	videoOf := func(id string) map[string]any {
		_, body := embyCall(t, ts, "GET", "/Items/"+id+"/PlaybackInfo", token, "")
		sources, _ := body["MediaSources"].([]any)
		if len(sources) == 0 {
			t.Fatalf("播放信息无媒体源: %v", body)
		}
		src, _ := sources[0].(map[string]any)
		streams, _ := src["MediaStreams"].([]any)
		for _, raw := range streams {
			s, _ := raw.(map[string]any)
			if s["Type"] == "Video" {
				return s
			}
		}
		t.Fatalf("媒体源无视频轨: %v", src)
		return nil
	}

	primary := videoOf("1")
	if primary["Width"].(float64) != 320 || primary["Height"].(float64) != 240 {
		t.Errorf("主文件分辨率 = %vx%v, want 320x240", primary["Width"], primary["Height"])
	}
	// AdditionalPart 的虚拟 id：part-<movieID>-<part>，CD2 → part-1-2
	part := videoOf("part-1-2")
	if part["Width"].(float64) != 640 || part["Height"].(float64) != 360 {
		t.Errorf("CD2 分辨率 = %vx%v, want 640x360（应来自它自己的 mediainfo.json，而非主文件 NFO）",
			part["Width"], part["Height"])
	}
	if part["BitDepth"].(float64) != 8 {
		t.Errorf("CD2 BitDepth = %v, want 8", part["BitDepth"])
	}
}
