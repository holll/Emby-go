package nfo

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// 标签级更新器：刮削器与手动编辑都走这条路写入 NFO。
//
// 为什么不用 Save/SaveAtomic：那是「读成结构体 → 整体重写」，会把 MovieMeta 未建模的
// 元素（其它刮削器写的扩展标签、注释、属性）静默丢掉。NFO 是元数据真源，
// 外部工具随时可能往里写东西，本服务只能动自己要动的那几个标签。
//
// 保留策略与 SaveFileInfo 一致：按原文定位做块级替换，其余内容字节级保留
// （含 BOM 与原有缩进），写入一律走 writeFileAtomic。

// ScrapeFields 一次刮削要写入 NFO 的元数据。
//
// 空值一律表示「本次没有这个字段」，**不删除**已有内容——刮削结果缺某个字段
// （接口没返回、翻译失败等）时，删掉用户手工填的值是数据损坏，不是覆盖。
type ScrapeFields struct {
	Number        string // 番号，写 <num>
	Title         string
	OriginalTitle string
	SortTitle     string
	Plot          string
	Year          int
	Premiered     string
	Rating        float64
	Mpaa          string
	Director      string
	Maker         string
	Label         string
	Runtime       int    // 分钟
	Series        string // 写 <set><name>，非空才动 <set>
	ProviderID    string // <uniqueid type="metatube">
	TrailerURL    string // <uniqueid type="trailerurl">

	Genres   []string
	Tags     []string
	Studios  []string
	Taglines []string
	Actors   []Actor

	// Overwrite=false 为「只补缺失」：某个字段在原文里已有非空值就整块跳过。
	// 列表字段同理——列表非空即视为已有，不做并集（避免中英类型混杂）。
	Overwrite bool

	// ManualEdit 仅由人工编辑入口设置：绕过已有字段锁（仍遵守 Overwrite），
	// 并允许修改以下锁配置。自动刮削即使 Overwrite=true 也必须遵守现有锁。
	ManualEdit bool
	// ClearFields contains direct scalar/list tag names explicitly cleared by
	// the strict manual PATCH adapter. Ignored unless ManualEdit is true.
	// Scrapers and the legacy PUT adapter never populate it.
	ClearFields []string
	// nil 保持原配置；非 nil 空列表解除字段锁。未知锁名原样保留。
	LockedFields *[]string
	// nil 保持原配置；非 nil 显式写入 true/false。
	LockData *bool
}

// scalarTags 单值标签：找到第一个同名标签替换内容，缺失则插到 </movie> 前。
var scalarTags = []string{
	"num", "title", "originaltitle", "sorttitle", "plot", "year", "premiered",
	"rating", "mpaa", "director", "maker", "label", "runtime",
}

// listTags 列表标签：整类清空后按新值重建（块级替换）。
var listTags = []string{"genre", "tag", "studio", "tagline"}

// UpdateScraped 把刮削结果写入 NFO。文件不存在时按新建处理（整体生成）。
func UpdateScraped(path string, f ScrapeFields) error {
	// 与 SetActorThumb / SaveFileInfo 共用同一把路径锁：整段读-改-写期间独占，
	// 新建分支走 saveAtomicLocked（本函数已持锁，不能再进 SaveAtomic）。
	defer lockNFOPath(path)()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return saveAtomicLocked(path, metaFromScrape(f))
	}
	if err != nil {
		return err
	}
	text := string(raw)
	if _, err := findClosingMovie(text); err != nil {
		return err
	}
	indent := childIndent(text)
	// 已持有路径锁，读取本次最新原文的锁；禁止用调用方缓存的元数据判定。
	locked := lockedTags(text, f.ManualEdit)
	clear := map[string]bool{}
	if f.ManualEdit {
		for _, tag := range f.ClearFields {
			clear[tag] = true
		}
	}

	// 单值标签
	for _, tag := range scalarTags {
		value := scalarValue(f, tag)
		if locked[tag] || (value == "" && !clear[tag]) || (hasScalar(text, tag) && !f.Overwrite) {
			continue
		}
		if value == "" {
			text = replaceBlocks(text, tag, nil, indent)
		} else {
			text = upsertScalar(text, tag, value, indent)
		}
	}

	// 列表标签
	for _, tag := range listTags {
		values := listValue(f, tag)
		if locked[tag] || (len(values) == 0 && !clear[tag]) || (hasAnyBlock(text, tag) && !f.Overwrite) {
			continue
		}
		text = replaceBlocks(text, tag, renderList(tag, values, indent), indent)
	}

	// 演员（带子元素，单独渲染）
	if !locked["actor"] && len(f.Actors) > 0 && (f.Overwrite || !hasAnyBlock(text, "actor")) {
		text = replaceBlocks(text, "actor", renderActors(f.Actors, indent), indent)
	}

	// 合集 <set><name>：非空才动，避免把别的工具写的 <set> 拆掉。
	if series := strings.TrimSpace(f.Series); !locked["set"] && series != "" && (f.Overwrite || !hasAnyBlock(text, "set")) {
		text = replaceBlocks(text, "set", []string{renderSet(series, indent)}, indent)
	}

	// uniqueid 按 type 定位，不动其它 type（trailerurl 等由别的来源维护时不能误删）。
	if id := strings.TrimSpace(f.ProviderID); !locked["uniqueid"] && id != "" && (f.Overwrite || !hasUniqueID(text, "metatube")) {
		text = upsertUniqueID(text, "metatube", id, indent)
	}
	if url := strings.TrimSpace(f.TrailerURL); !locked["uniqueid"] && url != "" && (f.Overwrite || !hasUniqueID(text, "trailerurl")) {
		text = upsertUniqueID(text, "trailerurl", url, indent)
	}

	if f.ManualEdit {
		if f.LockedFields != nil {
			text = replaceBlocks(text, "lockedfields", nil, indent)
			if names := trimAll(*f.LockedFields); len(names) > 0 {
				text = upsertScalar(text, "lockedfields", strings.Join(names, "|"), indent)
			}
		}
		if f.LockData != nil {
			text = upsertScalar(text, "lockdata", strconv.FormatBool(*f.LockData), indent)
		}
	}
	if text == string(raw) {
		return nil
	}
	return writeFileAtomic(path, text)
}

// lockedTags 将 Emby canonical 字段锁映射到可更新的 movie 直系标签。
// 未识别的锁不影响其它字段，且自动更新永不改写锁原文。
func lockedTags(text string, manual bool) map[string]bool {
	out := make(map[string]bool)
	if manual {
		return out
	}
	mapping := map[string][]string{
		"Name": {"title"}, "Number": {"num"}, "OriginalTitle": {"originaltitle"},
		"Overview": {"plot"}, "ProductionYear": {"year"}, "Genres": {"genre"},
		"Tags": {"tag"}, "Studios": {"studio"}, "Maker": {"maker"}, "Label": {"label"},
		"SortName": {"sorttitle"}, "PremiereDate": {"premiered"},
		"CommunityRating": {"rating"}, "OfficialRating": {"mpaa"},
		"People": {"actor", "director"}, "Runtime": {"runtime"},
		"Collections": {"set"}, "ProviderIds": {"uniqueid"}, "Taglines": {"tagline"},
	}
	all := strings.EqualFold(ScrapedTagValue(text, "lockdata"), "true")
	for name := range strings.SplitSeq(ScrapedTagValue(text, "lockedfields"), "|") {
		for field, tags := range mapping {
			if strings.EqualFold(strings.TrimSpace(name), field) {
				for _, tag := range tags {
					out[tag] = true
				}
			}
		}
	}
	if all {
		for _, tags := range mapping {
			for _, tag := range tags {
				out[tag] = true
			}
		}
	}
	return out
}

// metaFromScrape 在 NFO 不存在时把刮削结果转成完整元数据（仅用于新建文件）。
func metaFromScrape(f ScrapeFields) MovieMeta {
	meta := MovieMeta{
		Number: f.Number, Title: f.Title, OriginalTitle: f.OriginalTitle, SortTitle: f.SortTitle, Plot: f.Plot,
		Year: f.Year, Premiered: f.Premiered, Rating: f.Rating, Mpaa: f.Mpaa,
		Director: f.Director, Maker: f.Maker, Label: f.Label, Runtime: int64(f.Runtime),
		Genres: f.Genres, Tags: f.Tags, Studios: f.Studios, Taglines: f.Taglines,
		Actors: f.Actors,
	}
	if f.ManualEdit {
		if f.LockedFields != nil {
			meta.LockedFields = strings.Join(trimAll(*f.LockedFields), "|")
		}
		if f.LockData != nil {
			meta.LockData = strconv.FormatBool(*f.LockData)
		}
	}
	if strings.TrimSpace(f.Series) != "" {
		meta.Set = &MovieSet{Name: f.Series}
	}
	if strings.TrimSpace(f.ProviderID) != "" {
		meta.UniqueIDs = append(meta.UniqueIDs, UniqueID{Type: "metatube", Value: f.ProviderID})
	}
	if strings.TrimSpace(f.TrailerURL) != "" {
		meta.UniqueIDs = append(meta.UniqueIDs, UniqueID{Type: "trailerurl", Value: f.TrailerURL})
	}
	return meta
}

func scalarValue(f ScrapeFields, tag string) string {
	switch tag {
	case "num":
		return strings.TrimSpace(f.Number)
	case "title":
		return strings.TrimSpace(f.Title)
	case "originaltitle":
		return strings.TrimSpace(f.OriginalTitle)
	case "sorttitle":
		return strings.TrimSpace(f.SortTitle)
	case "plot":
		return strings.TrimSpace(f.Plot)
	case "year":
		if f.Year > 0 {
			return strconv.Itoa(f.Year)
		}
	case "premiered":
		return strings.TrimSpace(f.Premiered)
	case "rating":
		if f.Rating > 0 {
			return strconv.FormatFloat(f.Rating, 'f', 1, 64)
		}
	case "mpaa":
		return strings.TrimSpace(f.Mpaa)
	case "director":
		return strings.TrimSpace(f.Director)
	case "maker":
		return strings.TrimSpace(f.Maker)
	case "label":
		return strings.TrimSpace(f.Label)
	case "runtime":
		if f.Runtime > 0 {
			return strconv.Itoa(f.Runtime)
		}
	}
	return ""
}

func listValue(f ScrapeFields, tag string) []string {
	switch tag {
	case "genre":
		return trimAll(f.Genres)
	case "tag":
		return trimAll(f.Tags)
	case "studio":
		return trimAll(f.Studios)
	case "tagline":
		return trimAll(f.Taglines)
	}
	return nil
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// directElement 的偏移来自 XML decoder，切片替换保留未修改内容的全部原始字节。
// 只选择无命名空间的直系元素；注释、CDATA、嵌套同名元素与扩展命名空间不参与匹配。
type directElement struct {
	start, end int
	name       xml.Name
	attrs      []xml.Attr
}

func directElements(text string) ([]directElement, int, xml.Name, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	depth, closing := 0, -1
	var root xml.Name
	var elements []directElement
	var current directElement
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, -1, root, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if root.Local != "" {
					return nil, -1, root, errors.New("nfo: 多个根元素")
				}
				root = token.Name
			}
			if depth == 1 {
				current = directElement{start: start, name: token.Name, attrs: token.Attr}
			}
			depth++
		case xml.EndElement:
			depth--
			if depth == 1 {
				current.end = int(decoder.InputOffset())
				elements = append(elements, current)
			}
			if depth == 0 {
				closing = start
			}
		}
	}
	if closing < 0 {
		return nil, -1, root, errors.New("nfo: 缺少闭合根元素")
	}
	return elements, closing, root, nil
}

func elementsNamed(text, tag string) []directElement {
	elements, _, _, err := directElements(text)
	if err != nil {
		return nil
	}
	out := make([]directElement, 0)
	for _, element := range elements {
		if element.name.Space == "" && element.name.Local == tag {
			out = append(out, element)
		}
	}
	return out
}

// findClosingMovie 同时验证全文 XML，避免将注释中的 </movie> 误认成根闭合标签。
func findClosingMovie(text string) (int, error) {
	_, closing, root, err := directElements(text)
	if err != nil {
		return 0, err
	}
	if root.Space != "" || root.Local != "movie" || !strings.HasPrefix(text[closing:], "</movie") {
		return 0, errors.New("nfo: 未找到 </movie>，拒绝改写")
	}
	return closing, nil
}

// childIndent 取原文顶层子标签的缩进，新插入的标签沿用同一风格（默认 2 空格）。
func childIndent(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || !strings.HasPrefix(trimmed, "<") {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "<?"), strings.HasPrefix(trimmed, "<movie"),
			strings.HasPrefix(trimmed, "</movie"), strings.HasPrefix(trimmed, "<!--"):
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}

func escape(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

// hasScalar 判断原文里是否已有该标签且内容非空。
func hasScalar(text, tag string) bool {
	return ScrapedTagValue(text, tag) != ""
}

// upsertScalar 替换首个同名标签的内容；不存在则插到 </movie> 前。
func upsertScalar(text, tag, value, indent string) string {
	return upsertScalarIn(text, tag, value, indent, "</movie>")
}

// upsertScalarIn 与 upsertScalar 相同，但可指定插入位置对应的闭合标签
// （如在 <actor> 块内补 <thumb> 时用 </actor>）。
// 替换时连同行首缩进一起吃掉再重写，否则旧缩进会与新缩进叠加。
func upsertScalarIn(text, tag, value, indent, beforeClose string) string {
	replacement := "<" + tag + ">" + escape(value) + "</" + tag + ">"
	elements := elementsNamed(text, tag)
	for _, element := range slices.Backward(elements) {
		text = text[:element.start] + replacement + text[element.end:]
	}
	if len(elements) > 0 {
		return text
	}
	return insertBeforeTag(text, beforeClose, indent+replacement)
}

// scalarLinePattern 在 scalarPattern 基础上捕获行首缩进（$1），供替换时保留缩进风格。
func scalarLinePattern(tag string) *regexp.Regexp {
	name := regexp.QuoteMeta(tag)
	return regexp.MustCompile(`(?m)^([ \t]*)(?:<` + name + `(?:\s[^>]*)?>.*?</` + name + `\s*>|<` + name + `(?:\s[^>]*)?/>)`)
}

// hasAnyBlock 判断原文里是否至少有一个该标签块。
func hasAnyBlock(text, tag string) bool {
	return len(elementsNamed(text, tag)) > 0
}

// blockPattern 匹配成对标签块（不匹配自闭合，标签块都带子元素）。
func blockPattern(tag string) *regexp.Regexp {
	name := regexp.QuoteMeta(tag)
	return regexp.MustCompile(`(?s)<` + name + `(?:\s[^>]*)?>.*?</` + name + `\s*>`)
}

// replaceBlocks 删除根元素的同名直系块（包括紧凑 XML 与自闭合标签）。
// 仅当标签独占一行时清除该行空白，不吞相邻标签、注释或扩展内容。
func replaceBlocks(text, tag string, rendered []string, indent string) string {
	elements := elementsNamed(text, tag)
	for _, element := range slices.Backward(elements) {
		start, end := element.start, element.end
		lineStart := strings.LastIndexByte(text[:start], '\n') + 1
		lineEnd := strings.IndexByte(text[end:], '\n')
		if lineEnd >= 0 && strings.TrimSpace(text[lineStart:start]) == "" && strings.TrimSpace(text[end:end+lineEnd]) == "" {
			start, end = lineStart, end+lineEnd+1
		}
		text = text[:start] + text[end:]
	}
	block := strings.TrimRight(strings.Join(rendered, ""), "\n")
	if block == "" {
		return text
	}
	return insertBeforeClosingMovie(text, block, indent)
}

// insertBeforeClosingMovie 把内容插到 </movie> 之前（内容自带缩进）。
func insertBeforeClosingMovie(text, content, indent string) string {
	if _, err := findClosingMovie(text); err != nil {
		return text
	}
	return insertBeforeTag(text, "</movie>", content)
}

// insertBeforeTag 把内容插到指定闭合标签所在行的行首之前，避免新块被塞在闭合标签的缩进之后。
func insertBeforeTag(text, closing, content string) string {
	_, index, root, err := directElements(text)
	if err != nil || closing != "</"+root.Local+">" {
		return text
	}
	insertAt := strings.LastIndexByte(text[:index], '\n') + 1
	if strings.TrimSpace(text[insertAt:index]) != "" {
		// 闭合标签与其它内容同行，只能就地插入。
		insertAt = index
	}
	return text[:insertAt] + content + "\n" + text[insertAt:]
}

func renderActors(actors []Actor, indent string) []string {
	out := make([]string, 0, len(actors))
	for _, actor := range actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		var builder strings.Builder
		builder.WriteString(indent + "<actor>\n")
		writeValue(&builder, len(indent)+2, "name", name)
		writeValue(&builder, len(indent)+2, "type", firstNonEmpty(actor.Type, "Actor"))
		writeValue(&builder, len(indent)+2, "metatubeid", actor.MetaTubeID)
		writeValue(&builder, len(indent)+2, "thumb", actor.Thumb)
		builder.WriteString(indent + "</actor>\n")
		out = append(out, builder.String())
	}
	return out
}

// SetActorThumb 更新指定演员的 <thumb>（头像真源，需求 A7 #44）。
//
// 只改目标演员块内的 <thumb>，其它演员块与全部未知标签原样保留；
// 演员不存在或已是同值时不做任何写入，返回 false。
// 之所以不做「读出全部演员 → 整体重写 actor 块」：那会丢掉我们没建模的 actor 子元素。
func SetActorThumb(path, name, thumb string) (bool, error) {
	// 头像任务按演员并发，多个演员共演同一部片时会对同一 NFO 撞写，必须串行化。
	defer lockNFOPath(path)()
	name = strings.TrimSpace(name)
	thumb = strings.TrimSpace(thumb)
	if name == "" || thumb == "" {
		return false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	text := string(raw)
	if lockedTags(text, false)["actor"] {
		return false, nil
	}
	changed := false
	for _, element := range elementsNamed(text, "actor") {
		block := text[element.start:element.end]
		if ScrapedTagValue(block, "name") != name {
			continue
		}
		if ScrapedTagValue(block, "thumb") == thumb {
			return false, nil
		}
		replaced := upsertScalarIn(block, "thumb", thumb, actorChildIndent(block), "</actor>")
		text = text[:element.start] + replaced + text[element.end:]
		changed = true
		break
	}
	if !changed {
		return false, nil
	}
	return true, writeFileAtomic(path, text)
}

// actorChildIndent 取 actor 块内子标签的缩进（默认比 <actor> 深 2 空格）。
func actorChildIndent(block string) string {
	for _, line := range strings.Split(block, "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "<") && !strings.HasPrefix(trimmed, "</") {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "    "
}

// firstNonEmpty 返回第一个非空字符串（标签值兜底用）。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// renderList 把一组值渲染成同名标签块（每个值一行）。
func renderList(tag string, values []string, indent string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, indent+"<"+tag+">"+escape(value)+"</"+tag+">\n")
	}
	return out
}

func renderSet(name, indent string) string {
	var builder strings.Builder
	builder.WriteString(indent + "<set>\n")
	writeValue(&builder, len(indent)+2, "name", name)
	builder.WriteString(indent + "</set>\n")
	return builder.String()
}

func hasUniqueID(text, kind string) bool {
	return len(uniqueIDElements(text, kind)) > 0
}

func uniqueIDPattern(kind string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<uniqueid\s[^>]*type\s*=\s*"` + regexp.QuoteMeta(kind) + `"[^>]*>.*?</uniqueid\s*>`)
}

// upsertUniqueID 按 type 替换 <uniqueid>；同名 type 不存在则新增。
func upsertUniqueID(text, kind, value, indent string) string {
	replacement := `<uniqueid type="` + kind + `">` + escape(value) + "</uniqueid>"
	elements := uniqueIDElements(text, kind)
	for _, element := range slices.Backward(elements) {
		text = text[:element.start] + replacement + text[element.end:]
	}
	if len(elements) > 0 {
		return text
	}
	return insertBeforeClosingMovie(text, indent+replacement, indent)
}

func uniqueIDElements(text, kind string) []directElement {
	var out []directElement
	for _, element := range elementsNamed(text, "uniqueid") {
		for _, attr := range element.attrs {
			if attr.Name.Space == "" && attr.Name.Local == "type" && strings.EqualFold(attr.Value, kind) {
				out = append(out, element)
				break
			}
		}
	}
	return out
}

// ScrapedTagValue 读取 NFO 里某个单值标签的当前内容（供「只补缺失」预览与 diff）。
func ScrapedTagValue(text, tag string) string {
	for _, element := range elementsNamed(text, tag) {
		var value string
		if err := xml.Unmarshal([]byte(text[element.start:element.end]), &value); err == nil {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// TagPreview 返回 NFO 原文里若干单值标签的当前值（缺失为空串），
// 供单条手动刮削的「与现有值 diff」展示。
func TagPreview(path string, tags []string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	text := string(raw)
	out := make(map[string]string, len(tags))
	for _, tag := range tags {
		out[tag] = ScrapedTagValue(text, tag)
	}
	return out, nil
}

// ListPreview 返回多个列表标签的当前值与演员名清单，供预览 diff。
func ListPreview(path string) (map[string][]string, []Actor, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]string{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	text := string(raw)
	out := make(map[string][]string, len(listTags))
	for _, tag := range listTags {
		for _, element := range elementsNamed(text, tag) {
			var value string
			if err := xml.Unmarshal([]byte(text[element.start:element.end]), &value); err == nil && strings.TrimSpace(value) != "" {
				out[tag] = append(out[tag], strings.TrimSpace(value))
			}
		}
	}
	actors, err := LoadActors(text)
	if err != nil {
		return out, nil, err
	}
	return out, actors, nil
}

// LoadActors 解析原文里的 <actor> 块（含 <thumb>），用于头像真源的回读。
func LoadActors(text string) ([]Actor, error) {
	var out []Actor
	for _, element := range elementsNamed(text, "actor") {
		var actor Actor
		if err := xml.Unmarshal([]byte(text[element.start:element.end]), &actor); err != nil {
			// 单个 actor 块解析失败不该让整次读取失败，跳过即可。
			continue
		}
		if strings.TrimSpace(actor.Name) != "" {
			out = append(out, actor)
		}
	}
	return out, nil
}

// FormatScrapeDiff 生成「字段：旧值 → 新值」的文本 diff，供单条手动刮削预览。
// 只在值确实变化时输出一行；旧值为空记为「（空）」。
func FormatScrapeDiff(old map[string]string, f ScrapeFields) []string {
	out := make([]string, 0, len(scalarTags))
	for _, tag := range scalarTags {
		current := strings.TrimSpace(old[tag])
		next := scalarValue(f, tag)
		if next == "" || next == current {
			continue
		}
		label := current
		if label == "" {
			label = "（空）"
		}
		out = append(out, fmt.Sprintf("%s: %s → %s", tag, label, next))
	}
	for _, tag := range listTags {
		values := listValue(f, tag)
		if len(values) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s", tag, strings.Join(values, " / ")))
	}
	return out
}
