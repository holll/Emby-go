package scraper

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/metatube"
	"emby-go/internal/nfo"
	"emby-go/internal/store"
	"emby-go/internal/translate"
)

// —— 纯函数：搜索词与结果选择 ——

func TestSearchQueryPriority(t *testing.T) {
	cases := []struct {
		name  string
		movie store.Movie
		want  string
	}{
		{"优先用 DB 番号", store.Movie{Number: "ABF-018", Title: "标题", SourcePath: "/m/xxx.strm"}, "ABF-018"},
		{"无番号时从文件名提取", store.Movie{SourcePath: "/m/ABF-018.mp4.strm"}, "ABF-018"},
		{"都没有时用标题", store.Movie{Title: "某标题", SourcePath: "/m/random.strm"}, "random"},
		{"全空返回空串", store.Movie{SourcePath: ".strm"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchQuery(tc.movie); got != tc.want {
				t.Errorf("searchQuery = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestSelectExactOnlyAcceptsNumberMatch(t *testing.T) {
	results := []metatube.MovieSearchResult{
		{Provider: "a", ID: "1", Number: "ABF-019"},
		{Provider: "b", ID: "2", Number: "ABF-018"},
	}
	chosen, ok := selectExact(results, "ABF-018")
	if !ok || chosen.Provider != "b" {
		t.Fatalf("应选中番号一致的那条: %+v %v", chosen, ok)
	}
	// 无精确命中时不能退回首个（否则会把错误影片的元数据写进去）。
	if _, ok := selectExact(results, "ABF-020"); ok {
		t.Error("无精确命中时不应选中任何结果")
	}
	if _, ok := selectExact(results, ""); ok {
		t.Error("预期番号为空时不应选中")
	}
	// 分隔符差异视为同一部。
	if chosen, ok := selectExact(results, "ABF_018"); !ok || chosen.Provider != "b" {
		t.Errorf("分隔符差异应视为同一番号: %+v %v", chosen, ok)
	}
}

func TestPickCandidatesPrefersExact(t *testing.T) {
	results := make([]metatube.MovieSearchResult, 0, 8)
	for i := 0; i < 8; i++ {
		results = append(results, metatube.MovieSearchResult{Provider: "p", ID: string(rune('a' + i)), Number: "ZZZ-00" + string(rune('0'+i))})
	}
	// 精确命中的那条排在第 7 位，截断后必须仍在结果里。
	results[6].Number = "ABF-018"
	picked := pickCandidates(results, "ABF-018", 5)
	if len(picked) != 5 {
		t.Fatalf("候选数 = %d，期望 5", len(picked))
	}
	if picked[0].Number != "ABF-018" {
		t.Errorf("精确命中应排在最前，得到 %+v", picked[0])
	}
}

// —— 字段映射 ——

func TestBuildFieldsMapsMetaTubeToNFO(t *testing.T) {
	info := metatube.MovieInfo{
		ID: "abc", Number: "ABF-018", Title: "原文标题", Provider: "fanza",
		Summary: "原文简介", Director: "导演", Maker: "厂商", Label: "厂牌",
		Series: "系列", Genres: []string{"剧情"}, Score: 8.5, Runtime: 120,
		ReleaseDate: "2024-03-05", Actors: []string{"甲", " ", "乙"},
	}
	fields := buildFields(info, "中文标题", "中文简介", true)

	// <title>/<sorttitle> 一律是「番号 标题」，原文另存 originaltitle。
	if fields.Title != "ABF-018 中文标题" || fields.SortTitle != "ABF-018 中文标题" {
		t.Errorf("标题/排序标题应拼番号前缀: %+v", fields)
	}
	if fields.OriginalTitle != "原文标题" {
		t.Errorf("原文标题应保留原文: %+v", fields)
	}
	if fields.Plot != "中文简介" || fields.Year != 2024 || fields.Premiered != "2024-03-05" {
		t.Errorf("简介/日期映射不对: %+v", fields)
	}
	if fields.Rating != 8.5 {
		t.Errorf("评分应按原值不放大: %v", fields.Rating)
	}
	if fields.Runtime != 120 {
		t.Errorf("时长(分钟) = %d", fields.Runtime)
	}
	if fields.Mpaa != officialRating {
		t.Errorf("分级应为常量 %s，得到 %s", officialRating, fields.Mpaa)
	}
	if fields.ProviderID != "metatube:fanza:abc" {
		t.Errorf("ProviderID = %q", fields.ProviderID)
	}
	if !contains(fields.Tags, "厂牌") {
		t.Errorf("label 应并入 tags: %v", fields.Tags)
	}
	if !contains(fields.Studios, "厂商") {
		t.Errorf("maker 应并入 studios: %v", fields.Studios)
	}
	if len(fields.Actors) != 2 {
		t.Errorf("空演员名应被剔除: %+v", fields.Actors)
	}
}

func TestJoinNumberTitle(t *testing.T) {
	cases := []struct {
		number, title, want string
	}{
		{"ABF-018", "标题", "ABF-018 标题"},
		{"", "标题", "标题"},
		{"ABF-018", "", "ABF-018"},
		{"", "", ""},
		{"ABF-018", " 标题 ", "ABF-018 标题"},
		// 上游标题已带番号时不再重复拼，而是把那段番号改写成归一化形式。
		{"ABF-018", "ABF-018 标题", "ABF-018 标题"},
		{"ABF-018", "ABF018 标题", "ABF-018 标题"},
		{"ABF-018", "abf_018 标题", "ABF-018 标题"},
		{"ABF-018", "abf-018（标题）", "ABF-018（标题）"},
		{"ABF-018", "abf-018. 标题", "ABF-018 标题"},
		{"ABF-018", "abf018 标题", "ABF-018 标题"},
		// 前缀一律随传入的番号形态（buildFields 传入的已是带破折号的规范番号）。
		{"ABF018", "ABF-018 标题", "ABF018 标题"},
		// 番号只是标题的前缀片段时不算已带番号（前端判定须与此一致）。
		{"ABF-018", "ABF-0182 标题", "ABF-018 ABF-0182 标题"},
		{"ABF-018", "ABF-018X 标题", "ABF-018 ABF-018X 标题"},
		{"ABF-018", "ABF-01 标题", "ABF-018 ABF-01 标题"},
		// 标题本身就是番号、或番号不在开头时。
		{"ABF-018", "ABF-018", "ABF-018"},
		{"ABF-018", "   ABF-018 标题  ", "ABF-018 标题"},
		{"ABF-018", "标题 ABF-018", "ABF-018 标题 ABF-018"},
	}
	for _, tc := range cases {
		if got := joinNumberTitle(tc.number, tc.title); got != tc.want {
			t.Errorf("joinNumberTitle(%q, %q) = %q，期望 %q", tc.number, tc.title, got, tc.want)
		}
	}
}

// TestBuildFieldsNormalizesNumber 番号归一化（大写、统一分隔符、补破折号）后写入 <num> 与标题前缀。
func TestBuildFieldsNormalizesNumber(t *testing.T) {
	cases := []struct {
		number, want string
	}{
		{"abf_018", "ABF-018"},
		{"abf 018", "ABF-018"},
		{"ABF-018", "ABF-018"},
		{"abf--018", "ABF-018"},
		{"abf018", "ABF-018"},
		{"SSIS00123", "SSIS-00123"},
		// 259LUXU 的数字前缀是来源站编号，不属于番号。
		{"259LUXU1234", "LUXU-1234"},
		{"259LUXU", "259LUXU"},
		// T28 系列名自带数字，须补在系列名之后（通用规则会错补成 T-28036）。
		{"T28036", "T28-036"},
		{"T28-036", "T28-036"},
		// 已有分隔符、断点无法判断或纯数字的番号不猜结构，原样保留。
		{"FC2PPV1234567", "FC2PPV1234567"},
		{"H4610", "H4610"},
		{"ABF-018-CD1", "ABF-018-CD1"},
		{"123456789", "123456789"},
		{"", ""},
	}
	for _, tc := range cases {
		info := metatube.MovieInfo{ID: "abc", Number: tc.number, Title: "原文标题", Provider: "fanza"}
		fields := buildFields(info, "中文标题", "", true)
		if fields.Number != tc.want {
			t.Errorf("buildFields(%q).Number = %q，期望 %q", tc.number, fields.Number, tc.want)
		}
		wantTitle := "中文标题"
		if tc.want != "" {
			wantTitle = tc.want + " 中文标题"
		}
		if fields.Title != wantTitle || fields.SortTitle != wantTitle {
			t.Errorf("buildFields(%q) 标题 = %q / %q，期望 %q", tc.number, fields.Title, fields.SortTitle, wantTitle)
		}
	}
}

func TestSplitReleaseDate(t *testing.T) {
	cases := []struct {
		in        string
		premiered string
		year      int
	}{
		{"2024-03-05", "2024-03-05", 2024},
		{"2024-03-05T00:00:00Z", "2024-03-05", 2024},
		{"", "", 0},
		{"not a date", "", 0},
	}
	for _, tc := range cases {
		premiered, year := splitReleaseDate(tc.in)
		if premiered != tc.premiered || year != tc.year {
			t.Errorf("splitReleaseDate(%q) = (%q, %d)，期望 (%q, %d)", tc.in, premiered, year, tc.premiered, tc.year)
		}
	}
}

func TestSplitProviderID(t *testing.T) {
	provider, id, ok := SplitProviderID("metatube:fanza:abc-1")
	if !ok || provider != "fanza" || id != "abc-1" {
		t.Errorf("SplitProviderID = (%q, %q, %v)", provider, id, ok)
	}
	for _, bad := range []string{"", "fanza:abc", "metatube:", "metatube:fanza", "metatube::abc"} {
		if _, _, ok := SplitProviderID(bad); ok {
			t.Errorf("%q 不应解析成功", bad)
		}
	}
}

// —— 演员名归一化 ——

func TestFoldNameAndMatchActor(t *testing.T) {
	results := []metatube.ActorSearchResult{
		{Name: "三上悠亜", Provider: "fanza", Aliases: []string{"みかみ ゆあ"}, Images: []string{"https://i/1.jpg"}},
		{Name: "Other", Provider: "x"},
	}
	if match, ok := matchActor(results, "三上悠亜"); !ok || match.Provider != "fanza" {
		t.Error("同名应命中")
	}
	if match, ok := matchActor(results, "みかみゆあ"); !ok || match.Provider != "fanza" {
		t.Errorf("别名（去空白后）应命中: %v", ok)
	}
	if match, ok := matchActor(results, "ＳＡＭＥ"); ok {
		t.Errorf("无关名字不应命中: %+v", match)
	}
	// 全角/半角与大小写差异要归一化掉。
	full := []metatube.ActorSearchResult{{Name: "ＡＢＣ", Provider: "p"}}
	if _, ok := matchActor(full, "abc"); !ok {
		t.Error("全角姓名应能匹配半角查询（反之亦然）")
	}
	if foldName(" 三上 悠亜 ") != foldName("三上悠亜") {
		t.Error("空白应被忽略")
	}
}

// —— 落盘（NFO + 图片）——

// fakeBackend 假 MetaTube：/v1/... 返回 JSON，/v1/images/... 返回 JPEG。
func fakeBackend(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/images/") {
			img := image.NewRGBA(image.Rect(0, 0, 4, 4))
			img.Set(0, 0, color.RGBA{R: 200, G: 100, A: 255})
			w.Header().Set("Content-Type", "image/jpeg")
			_ = jpeg.Encode(w, img, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/movies/search"):
			_, _ = w.Write([]byte(`{"data":[{"id":"abc","number":"ABF-018","title":"原文标题","provider":"fanza",
				"cover_url":"https://c/1.jpg","thumb_url":"https://t/1.jpg","score":9.1}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/movies/"):
			_, _ = w.Write([]byte(`{"data":{"id":"abc","provider":"fanza","number":"ABF-018","title":"原文标题",
				"summary":"原文简介","maker":"厂商","label":"厂牌","series":"系列","genres":["剧情"],"score":8.5,
				"runtime":120,"release_date":"2024-03-05","actors":["甲","乙"],
				"cover_url":"https://c/1.jpg","thumb_url":"https://t/1.jpg"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"nope"}}`))
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newTestScraper(t *testing.T, backendURL string, cfg Config) *Scraper {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg.BaseURL = backendURL
	cfg.Token = "token"
	cfg.DownloadImages = true
	return New(cfg, st)
}

func TestApplyWritesNFOAndImages(t *testing.T) {
	backend := fakeBackend(t)
	dir := t.TempDir()
	nfoPath := filepath.Join(dir, "ABF-018.nfo")
	// 存量 NFO 带外部工具的扩展标签与探测结果，刮削后都必须原样保留。
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>旧标题</title>
  <unknownvendor>keep</unknownvendor>
  <fileinfo>
    <size>123456</size>
    <probeversion>1</probeversion>
    <streamdetails>
      <video><codec>h264</codec><width>1920</width><height>1080</height></video>
    </streamdetails>
  </fileinfo>
</movie>
`
	if err := os.WriteFile(nfoPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	movie := store.Movie{
		ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "ABF-018.strm"),
		OutputDir: dir, NFOPath: nfoPath, Number: "ABF-018", Title: "旧标题", Status: "pending",
	}

	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	info, err := scraper.MovieInfo(context.Background(), "fanza", "abc")
	if err != nil {
		t.Fatalf("MovieInfo: %v", err)
	}
	// 关掉翻译，聚焦落盘逻辑。
	scraper.cfg.Translate = translate.Config{}

	result, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{
		Provider: "fanza", ID: "abc", Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.ProviderID != "metatube:fanza:abc" || result.ImageCount != 3 {
		t.Errorf("结果不对: %+v", result)
	}

	raw, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatal(err)
	}
	nfoText := string(raw)
	for _, want := range []string{
		"<title>ABF-018 原文标题</title>", // 番号前缀 + 未开翻译 → 原文
		"<sorttitle>ABF-018 原文标题</sorttitle>",
		"<originaltitle>原文标题</originaltitle>",
		"<plot>原文简介</plot>",
		"<mpaa>JP-18+</mpaa>",
		"<uniqueid type=\"metatube\">metatube:fanza:abc</uniqueid>",
		"<name>甲</name>",
		"<unknownvendor>keep</unknownvendor>", // 未知标签保留
		"<fileinfo>",                          // 探测结果保留
		"<codec>h264</codec>",
	} {
		if !strings.Contains(nfoText, want) {
			t.Errorf("NFO 缺少 %q:\n%s", want, nfoText)
		}
	}
	// 三张图都落盘为 webp。
	for _, name := range []string{"poster.webp", "fanart.webp", "landscape.webp"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			t.Errorf("%s 未落盘: %v", name, err)
		}
	}
	// 生成的 NFO 必须能被解析回结构体。
	meta, err := nfo.Read(nfoPath)
	if err != nil {
		t.Fatalf("刮削后的 NFO 无法解析: %v", err)
	}
	if meta.ProviderID() != "metatube:fanza:abc" || meta.Mpaa != officialRating {
		t.Errorf("解析结果不对: %+v", meta)
	}
}

// TestApplyFillMissingKeepsExisting 只补缺失：已有值的字段与已有图片都不被改写。
func TestApplyFillMissingKeepsExisting(t *testing.T) {
	backend := fakeBackend(t)
	dir := t.TempDir()
	nfoPath := filepath.Join(dir, "ABF-018.nfo")
	existing := `<movie>
  <title>手工标题</title>
  <plot>手工简介</plot>
</movie>
`
	if err := os.WriteFile(nfoPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	// 已有海报：只补缺失时不能被覆盖。
	posterPath := filepath.Join(dir, "poster.webp")
	if err := os.WriteFile(posterPath, []byte("manual-poster"), 0o644); err != nil {
		t.Fatal(err)
	}
	movie := store.Movie{ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "ABF-018.strm"),
		OutputDir: dir, NFOPath: nfoPath, Number: "ABF-018", Status: "pending"}

	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	scraper.cfg.Translate = translate.Config{}
	info, err := scraper.MovieInfo(context.Background(), "fanza", "abc")
	if err != nil {
		t.Fatal(err)
	}
	result, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{Provider: "fanza", ID: "abc", Overwrite: false})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(nfoPath)
	text := string(raw)
	// 标题保持手工值；原文另写入 originaltitle（映射表要求的固定落位）。
	if !strings.Contains(text, "<title>手工标题</title>") || strings.Contains(text, "<title>原文标题</title>") {
		t.Errorf("只补缺失不应覆盖已有标题:\n%s", text)
	}
	if !strings.Contains(text, "<originaltitle>原文标题</originaltitle>") {
		t.Errorf("原文标题应写入 originaltitle:\n%s", text)
	}
	if !strings.Contains(text, "<plot>手工简介</plot>") {
		t.Errorf("只补缺失不应覆盖已有简介:\n%s", text)
	}
	// 空字段应被补上。
	if !strings.Contains(text, "<mpaa>JP-18+</mpaa>") || !strings.Contains(text, "<name>甲</name>") {
		t.Errorf("缺失字段应被补上:\n%s", text)
	}
	// 已有海报保持原样，其余图片照常补齐。
	if data, _ := os.ReadFile(posterPath); !bytes.Equal(data, []byte("manual-poster")) {
		t.Errorf("已存在的海报被覆盖: %q", string(data))
	}
	if result.ImageCount != 2 {
		t.Errorf("应只补 2 张缺失图片，实际 %d: %v", result.ImageCount, result.Images)
	}
}

// TestApplyCreatesNFOWhenMissing 没有 NFO 的条目（pending）按新建处理。
func TestApplyCreatesNFOWhenMissing(t *testing.T) {
	backend := fakeBackend(t)
	dir := t.TempDir()
	movie := store.Movie{ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "ABF-018.strm"),
		OutputDir: dir, Number: "ABF-018", Status: "pending"}

	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	scraper.cfg.Translate = translate.Config{}
	info, _ := scraper.MovieInfo(context.Background(), "fanza", "abc")
	if _, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{Provider: "fanza", ID: "abc", Overwrite: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	meta, err := nfo.Read(filepath.Join(dir, "ABF-018.nfo"))
	if err != nil {
		t.Fatalf("未生成可解析的 NFO: %v", err)
	}
	if meta.Title != "ABF-018 原文标题" || meta.SortTitle != "ABF-018 原文标题" || len(meta.Actors) != 2 {
		t.Errorf("新建 NFO 内容不对: %+v", meta)
	}
}

// TestApplyImageFailureIsNotFatal 图片失败不影响元数据落盘（图片尽力而为）。
func TestApplyImageFailureIsNotFatal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/images/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"no image"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"abc","provider":"fanza","number":"ABF-018","title":"T",
			"summary":"S","genres":["剧情"],"cover_url":"https://c/1.jpg"}}`))
	}))
	defer ts.Close()
	dir := t.TempDir()
	movie := store.Movie{ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "x.strm"),
		OutputDir: dir, Number: "ABF-018", Status: "pending"}

	scraper := newTestScraper(t, ts.URL, DefaultConfig())
	scraper.cfg.Translate = translate.Config{}
	info, _ := scraper.MovieInfo(context.Background(), "fanza", "abc")
	result, err := scraper.Apply(context.Background(), movie, info, ApplyOptions{Provider: "fanza", ID: "abc", Overwrite: true})
	if err != nil {
		t.Fatalf("图片失败不应让刮削整体失败: %v", err)
	}
	if result.ImageCount != 0 || len(result.ImageWarn) != 3 {
		t.Errorf("应记录 3 条图片 warn: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.nfo")); err != nil {
		t.Errorf("元数据仍应落盘: %v", err)
	}
}

// TestPreviewHasNoSideEffects 预览不翻译、不下图、不写盘（取消 = 零副作用）。
func TestPreviewHasNoSideEffects(t *testing.T) {
	backend := fakeBackend(t)
	dir := t.TempDir()
	movie := store.Movie{ID: 1, LibraryID: 1, SourcePath: filepath.Join(dir, "ABF-018.strm"),
		OutputDir: dir, Number: "ABF-018", Status: "pending"}

	scraper := newTestScraper(t, backend.URL, DefaultConfig())
	preview, err := scraper.Preview(context.Background(), movie)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(preview.Candidates) != 1 || preview.Candidates[0].Provider != "fanza" {
		t.Fatalf("候选不对: %+v", preview.Candidates)
	}
	if !preview.Candidates[0].Exact || preview.Recommended != 0 || !preview.Exact {
		t.Errorf("番号精确命中应被推荐: %+v", preview)
	}
	if preview.Candidates[0].Thumb == "" || !strings.Contains(preview.Candidates[0].Thumb, "/api/admin/scrape/image") {
		t.Errorf("缩略图应走本服务代理: %q", preview.Candidates[0].Thumb)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("预览不应产生任何文件: %v", entries)
	}
}

func TestProgressGuard(t *testing.T) {
	// 未配置时 Run 必须直接报错，不能静默跑成空任务。
	scraper := New(Config{}, nil)
	if _, err := scraper.Run(context.Background(), RunOptions{}); err == nil {
		t.Error("未配置 MetaTube 时应报错")
	}
	if DefaultConfig().Workers() != 2 {
		t.Errorf("默认并发 = %d，期望 2", DefaultConfig().Workers())
	}
	if got := (Config{Concurrency: 99}).Workers(); got != 8 {
		t.Errorf("并发上限 = %d，期望 8", got)
	}
	if DefaultConfig().Timeout() != 30*time.Second {
		t.Errorf("默认超时 = %v，期望 30s", DefaultConfig().Timeout())
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
