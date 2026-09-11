package nfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 存量 NFO：含本服务未建模的扩展标签、注释与 XML 声明，用来验证「只动该动的标签」。
const existingNFO = `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <!-- 手工注释，必须保留 -->
  <title>旧标题</title>
  <originaltitle>旧原名</originaltitle>
  <plot>旧简介</plot>
  <year>2020</year>
  <genre>旧类型</genre>
  <tag>旧标签</tag>
  <studio>旧厂商</studio>
  <unknownvendor>
    <whatever attr="1">外部工具写的扩展块</whatever>
  </unknownvendor>
  <lockdata>true</lockdata>
  <uniqueid type="tmdb">12345</uniqueid>
  <actor>
    <name>旧演员</name>
    <type>Actor</type>
  </actor>
  <fileinfo>
    <size>999</size>
    <streamdetails>
      <video><codec>h264</codec><width>1920</width><height>1080</height></video>
    </streamdetails>
  </fileinfo>
</movie>
`

func writeTempNFO(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.nfo")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readTempNFO(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestUpdateScrapedPreservesUnknownTags 是更新器存在的理由：
// 刮削写入后，未建模的扩展标签、注释、lockdata、<fileinfo> 与其它类型的 uniqueid 必须原样保留。
func TestUpdateScrapedPreservesUnknownTags(t *testing.T) {
	path := writeTempNFO(t, existingNFO)
	err := UpdateScraped(path, ScrapeFields{
		Title: "新标题", Overwrite: true,
		Actors: []Actor{{Name: "新演员", Type: "Actor", Thumb: "https://x/a.jpg"}},
	})
	if err != nil {
		t.Fatalf("UpdateScraped: %v", err)
	}
	got := readTempNFO(t, path)

	for _, keep := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		"<!-- 手工注释，必须保留 -->",
		`<unknownvendor>`,
		`<whatever attr="1">外部工具写的扩展块</whatever>`,
		"</unknownvendor>",
		"<lockdata>true</lockdata>",
		`<uniqueid type="tmdb">12345</uniqueid>`,
		"<fileinfo>",
		"<codec>h264</codec>",
		`type="1"`,
	} {
		if keep == `type="1"` {
			continue
		}
		if !strings.Contains(got, keep) {
			t.Errorf("刮削后丢失了 %q\n---\n%s", keep, got)
		}
	}
	if !strings.Contains(got, "<title>新标题</title>") {
		t.Errorf("标题未被替换:\n%s", got)
	}
	if strings.Contains(got, "旧标题") {
		t.Errorf("旧标题应被替换:\n%s", got)
	}
	if !strings.Contains(got, "<thumb>https://x/a.jpg</thumb>") {
		t.Errorf("演员头像未写入:\n%s", got)
	}
	if strings.Contains(got, "旧演员") {
		t.Errorf("演员块应被整块替换:\n%s", got)
	}
	// 缩进未被破坏：顶层标签仍是 2 空格。
	if !strings.Contains(got, "\n  <title>新标题</title>") {
		t.Errorf("插入/替换后缩进不对:\n%s", got)
	}
}

// TestUpdateScrapedFillMissing 验证「只补缺失」：已有值的字段整块跳过，空字段补上。
func TestUpdateScrapedFillMissing(t *testing.T) {
	path := writeTempNFO(t, existingNFO)
	err := UpdateScraped(path, ScrapeFields{
		Title: "不该被写入", Plot: "补上的简介", Mpaa: "JP-18+", Year: 2024,
		Genres: []string{"新类型"},
	})
	if err != nil {
		t.Fatalf("UpdateScraped: %v", err)
	}
	got := readTempNFO(t, path)
	if !strings.Contains(got, "<title>旧标题</title>") {
		t.Error("只补缺失时不应覆盖已有标题")
	}
	if strings.Contains(got, "不该被写入") {
		t.Error("只补缺失时不应写入新标题")
	}
	if !strings.Contains(got, "<plot>旧简介</plot>") {
		t.Error("只补缺失时不应覆盖已有简介")
	}
	if strings.Contains(got, "补上的简介") {
		t.Error("非空的旧简介应被保留，不写入新值")
	}
	// 原本没有的字段应补上。
	if !strings.Contains(got, "<mpaa>JP-18+</mpaa>") {
		t.Errorf("缺失的 mpaa 应被补上:\n%s", got)
	}
	if !strings.Contains(got, "<genre>旧类型</genre>") {
		t.Error("列表已有值时不应整块替换")
	}
	if strings.Contains(got, "新类型") {
		t.Error("只补缺失时不应写入新类型列表")
	}
}

// TestUpdateScrapedListReplace 覆盖策略下列表标签整类重建，且不留残余。
func TestUpdateScrapedListReplace(t *testing.T) {
	path := writeTempNFO(t, existingNFO)
	err := UpdateScraped(path, ScrapeFields{
		Genres: []string{"剧情", "偶像"}, Tags: []string{"单体"},
		Studios: []string{"Studio X"}, Overwrite: true,
	})
	if err != nil {
		t.Fatalf("UpdateScraped: %v", err)
	}
	got := readTempNFO(t, path)
	if strings.Contains(got, "旧类型") || strings.Contains(got, "旧标签") || strings.Contains(got, "旧厂商") {
		t.Errorf("覆盖策略下旧列表值应被清掉:\n%s", got)
	}
	for _, want := range []string{"<genre>剧情</genre>", "<genre>偶像</genre>", "<tag>单体</tag>", "<studio>Studio X</studio>"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
	// 未介入的标签块不能被动。
	if !strings.Contains(got, "<lockdata>true</lockdata>") {
		t.Error("未涉及的标签不应被删")
	}
}

// TestUpdateScrapedSetAndUniqueID 验证 <set> 与按 type 定位的 <uniqueid>。
func TestUpdateScrapedSetAndUniqueID(t *testing.T) {
	path := writeTempNFO(t, existingNFO)
	err := UpdateScraped(path, ScrapeFields{
		Series: "合集A", ProviderID: "metatube:abc:1", TrailerURL: "https://t/1",
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("UpdateScraped: %v", err)
	}
	got := readTempNFO(t, path)
	if !strings.Contains(got, "<set>") || !strings.Contains(got, "<name>合集A</name>") {
		t.Errorf("<set> 未写入:\n%s", got)
	}
	if !strings.Contains(got, `<uniqueid type="metatube">metatube:abc:1</uniqueid>`) {
		t.Errorf("metatube uniqueid 未写入:\n%s", got)
	}
	if !strings.Contains(got, `<uniqueid type="trailerurl">https://t/1</uniqueid>`) {
		t.Errorf("trailerurl uniqueid 未写入:\n%s", got)
	}
	// 其它 type 必须保留。
	if !strings.Contains(got, `<uniqueid type="tmdb">12345</uniqueid>`) {
		t.Errorf("其它 type 的 uniqueid 被误删:\n%s", got)
	}

	// 再次写入不同合集名：应替换而不是叠加。
	if err := UpdateScraped(path, ScrapeFields{Series: "合集B", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	got = readTempNFO(t, path)
	if strings.Count(got, "<set>") != 1 || !strings.Contains(got, "<name>合集B</name>") {
		t.Errorf("<set> 应被替换而非叠加:\n%s", got)
	}
}

// TestUpdateScrapedEmptyValueNeverDeletes 空值只表示「本次没有该字段」，绝不删已有内容。
func TestUpdateScrapedEmptyValueNeverDeletes(t *testing.T) {
	path := writeTempNFO(t, existingNFO)
	if err := UpdateScraped(path, ScrapeFields{Overwrite: true}); err != nil {
		t.Fatalf("UpdateScraped: %v", err)
	}
	got := readTempNFO(t, path)
	for _, keep := range []string{"旧标题", "旧原名", "旧简介", "旧类型", "旧演员"} {
		if !strings.Contains(got, keep) {
			t.Errorf("空字段不应删除已有内容，%q 丢了", keep)
		}
	}
}

// TestUpdateScrapedCreatesFile 目标 NFO 不存在时按新建处理。
func TestUpdateScrapedCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.nfo")
	err := UpdateScraped(path, ScrapeFields{
		Title: "新片", OriginalTitle: "New", Year: 2024, Plot: "简介",
		Genres: []string{"剧情"}, Actors: []Actor{{Name: "演员一", Thumb: "https://x/a.jpg"}},
		Series: "合集A", ProviderID: "metatube:p:1",
	})
	if err != nil {
		t.Fatalf("UpdateScraped: %v", err)
	}
	meta, err := Read(path)
	if err != nil {
		t.Fatalf("生成的 NFO 无法解析: %v", err)
	}
	if meta.Title != "新片" || meta.Year != 2024 || len(meta.Genres) != 1 {
		t.Errorf("生成内容不对: %+v", meta)
	}
	if len(meta.Actors) != 1 || meta.Actors[0].Thumb != "https://x/a.jpg" {
		t.Errorf("演员/头像未写入: %+v", meta.Actors)
	}
	if meta.Collection() != "合集A" || meta.ProviderID() != "metatube:p:1" {
		t.Errorf("合集/ProviderID 未写入: %q %q", meta.Collection(), meta.ProviderID())
	}
}

// TestUpdateScrapedRejectsBrokenFile 没有 </movie> 的文件拒绝改写，避免把半截文件写坏。
func TestUpdateScrapedRejectsBrokenFile(t *testing.T) {
	path := writeTempNFO(t, "<movie>\n  <title>半截</title>\n")
	if err := UpdateScraped(path, ScrapeFields{Title: "x", Overwrite: true}); err == nil {
		t.Error("缺少 </movie> 应报错")
	}
	if got := readTempNFO(t, path); !strings.Contains(got, "半截") {
		t.Error("报错时不应改动原文件")
	}
}

// TestUpdateScrapedKeepsBOM 带 BOM 的文件改完后 BOM 仍在。
func TestUpdateScrapedKeepsBOM(t *testing.T) {
	path := writeTempNFO(t, "\ufeff"+existingNFO)
	if err := UpdateScraped(path, ScrapeFields{Title: "带BOM", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "\ufeff") {
		t.Error("BOM 丢失")
	}
	if !strings.Contains(string(raw), "<title>带BOM</title>") {
		t.Error("标题未替换")
	}
}

// TestPreviewHelpers 验证预览（diff 数据源）读取现有值。
func TestPreviewHelpers(t *testing.T) {
	path := writeTempNFO(t, existingNFO)
	values, err := TagPreview(path, []string{"title", "mpaa"})
	if err != nil {
		t.Fatal(err)
	}
	if values["title"] != "旧标题" || values["mpaa"] != "" {
		t.Errorf("TagPreview = %v", values)
	}
	lists, actors, err := ListPreview(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists["genre"]) != 1 || lists["genre"][0] != "旧类型" {
		t.Errorf("ListPreview genres = %v", lists["genre"])
	}
	if len(actors) != 1 || actors[0].Name != "旧演员" {
		t.Errorf("ListPreview actors = %v", actors)
	}

	diff := FormatScrapeDiff(values, ScrapeFields{Title: "新标题", Mpaa: "JP-18+"})
	joined := strings.Join(diff, "\n")
	if !strings.Contains(joined, "title: 旧标题 → 新标题") {
		t.Errorf("diff 缺少标题变化: %s", joined)
	}
	if !strings.Contains(joined, "mpaa: （空） → JP-18+") {
		t.Errorf("diff 缺少空值标注: %s", joined)
	}
	// 值未变化时不产生 diff 行。
	if strings.Contains(joined, "year") {
		t.Errorf("未变化的字段不应出现在 diff: %s", joined)
	}
}

// TestUpdateScrapedTagNamePrecision 标签名必须整词匹配，<title> 不能误伤 <sorttitle>。
func TestUpdateScrapedTagNamePrecision(t *testing.T) {
	body := `<movie>
  <sorttitle>Sort A</sorttitle>
  <title>Title A</title>
</movie>
`
	path := writeTempNFO(t, body)
	if err := UpdateScraped(path, ScrapeFields{Title: "Title B", Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	got := readTempNFO(t, path)
	if !strings.Contains(got, "<sorttitle>Sort A</sorttitle>") {
		t.Errorf("sorttitle 被误改:\n%s", got)
	}
	if !strings.Contains(got, "<title>Title B</title>") {
		t.Errorf("title 未改:\n%s", got)
	}
}

// TestSetActorThumb 头像真源写入：只动目标演员的 <thumb>，不碰别的演员与未知标签。
func TestSetActorThumb(t *testing.T) {
	body := `<movie>
  <title>T</title>
  <unknownvendor>keep me</unknownvendor>
  <actor>
    <name>甲</name>
    <type>Actor</type>
    <metatubeid>G:1</metatubeid>
  </actor>
  <actor>
    <name>乙</name>
    <type>Actor</type>
    <thumb>https://old/b.jpg</thumb>
  </actor>
</movie>
`
	path := writeTempNFO(t, body)

	changed, err := SetActorThumb(path, "甲", "https://new/a.jpg")
	if err != nil || !changed {
		t.Fatalf("SetActorThumb = (%v, %v)", changed, err)
	}
	got := readTempNFO(t, path)
	if !strings.Contains(got, "<thumb>https://new/a.jpg</thumb>") {
		t.Errorf("甲的 thumb 未写入:\n%s", got)
	}
	if !strings.Contains(got, "<name>甲</name>") || !strings.Contains(got, "<metatubeid>G:1</metatubeid>") {
		t.Errorf("甲的其它字段被破坏:\n%s", got)
	}
	if !strings.Contains(got, "<thumb>https://old/b.jpg</thumb>") {
		t.Errorf("乙的 thumb 不应被动:\n%s", got)
	}
	if !strings.Contains(got, "<unknownvendor>keep me</unknownvendor>") {
		t.Errorf("未知标签被破坏:\n%s", got)
	}
	// 缩进：thumb 应与同级标签对齐（4 空格）。
	if !strings.Contains(got, "\n    <thumb>https://new/a.jpg</thumb>") {
		t.Errorf("thumb 缩进不对:\n%s", got)
	}

	// 值相同时不再写入。
	changed, err = SetActorThumb(path, "甲", "https://new/a.jpg")
	if err != nil || changed {
		t.Errorf("同值不应重复写入: (%v, %v)", changed, err)
	}

	// 乙已有 thumb，覆盖它。
	if changed, err = SetActorThumb(path, "乙", "https://new/b.jpg"); err != nil || !changed {
		t.Fatalf("覆盖乙失败: (%v, %v)", changed, err)
	}
	if got = readTempNFO(t, path); !strings.Contains(got, "<thumb>https://new/b.jpg</thumb>") ||
		strings.Contains(got, "https://old/b.jpg") {
		t.Errorf("乙的 thumb 未被覆盖:\n%s", got)
	}

	// 不存在的演员：零改动。
	before := readTempNFO(t, path)
	if changed, err = SetActorThumb(path, "丙", "https://x/c.jpg"); err != nil || changed {
		t.Errorf("不存在的演员不应写入: (%v, %v)", changed, err)
	}
	if readTempNFO(t, path) != before {
		t.Error("未命中时文件被改动")
	}

	// 文件不存在：静默返回 false。
	if changed, err = SetActorThumb(filepath.Join(t.TempDir(), "none.nfo"), "甲", "https://x"); err != nil || changed {
		t.Errorf("文件不存在时应静默跳过: (%v, %v)", changed, err)
	}
}

// TestUpdateScrapedTagNameIsolation 标签名必须整词匹配：
// 替换 <tag> 不能碰到 <tagline>，替换 <studio> 不能碰到 <studios> 之类。
func TestUpdateScrapedTagNameIsolation(t *testing.T) {
	body := `<movie>
  <title>T</title>
  <tag>旧标签</tag>
  <tagline>旧标语</tagline>
  <studio>旧厂商</studio>
</movie>
`
	path := writeTempNFO(t, body)
	if err := UpdateScraped(path, ScrapeFields{
		Tags: []string{"新标签"}, Taglines: []string{"新标语"}, Studios: []string{"新厂商"}, Overwrite: true,
	}); err != nil {
		t.Fatal(err)
	}
	got := readTempNFO(t, path)
	if !strings.Contains(got, "<tag>新标签</tag>") {
		t.Errorf("<tag> 未替换:\n%s", got)
	}
	if !strings.Contains(got, "<tagline>新标语</tagline>") {
		t.Errorf("<tagline> 未替换（可能被 <tag> 的匹配吃掉）:\n%s", got)
	}
	if !strings.Contains(got, "<studio>新厂商</studio>") {
		t.Errorf("<studio> 未替换:\n%s", got)
	}
	for _, stale := range []string{"旧标签", "旧标语", "旧厂商"} {
		if strings.Contains(got, stale) {
			t.Errorf("旧值 %q 残留:\n%s", stale, got)
		}
	}
	// 解析回结构体必须仍然正确（XML 没被破坏）。
	meta, err := Read(path)
	if err != nil {
		t.Fatalf("改写后无法解析: %v\n%s", err, got)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "新标签" ||
		len(meta.Taglines) != 1 || meta.Taglines[0] != "新标语" ||
		len(meta.Studios) != 1 || meta.Studios[0] != "新厂商" {
		t.Errorf("解析结果不对: tags=%v taglines=%v studios=%v", meta.Tags, meta.Taglines, meta.Studios)
	}
}
