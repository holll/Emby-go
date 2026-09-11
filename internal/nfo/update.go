package nfo

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"regexp"
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
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SaveAtomic(path, metaFromScrape(f))
	}
	if err != nil {
		return err
	}
	text := string(raw)
	if _, err := findClosingMovie(text); err != nil {
		return err
	}
	indent := childIndent(text)

	// 单值标签
	for _, tag := range scalarTags {
		value := scalarValue(f, tag)
		if value == "" || (hasScalar(text, tag) && !f.Overwrite) {
			continue
		}
		text = upsertScalar(text, tag, value, indent)
	}

	// 列表标签
	for _, tag := range listTags {
		values := listValue(f, tag)
		if len(values) == 0 || (hasAnyBlock(text, tag) && !f.Overwrite) {
			continue
		}
		text = replaceBlocks(text, tag, renderList(tag, values, indent), indent)
	}

	// 演员（带子元素，单独渲染）
	if len(f.Actors) > 0 && (f.Overwrite || !hasAnyBlock(text, "actor")) {
		text = replaceBlocks(text, "actor", renderActors(f.Actors, indent), indent)
	}

	// 合集 <set><name>：非空才动，避免把别的工具写的 <set> 拆掉。
	if series := strings.TrimSpace(f.Series); series != "" && (f.Overwrite || !hasAnyBlock(text, "set")) {
		text = replaceBlocks(text, "set", []string{renderSet(series, indent)}, indent)
	}

	// uniqueid 按 type 定位，不动其它 type（trailerurl 等由别的来源维护时不能误删）。
	if id := strings.TrimSpace(f.ProviderID); id != "" && (f.Overwrite || !hasUniqueID(text, "metatube")) {
		text = upsertUniqueID(text, "metatube", id, indent)
	}
	if url := strings.TrimSpace(f.TrailerURL); url != "" && (f.Overwrite || !hasUniqueID(text, "trailerurl")) {
		text = upsertUniqueID(text, "trailerurl", url, indent)
	}

	return writeFileAtomic(path, text)
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

// findClosingMovie 返回 </movie> 的位置；没有闭合标签说明文件不完整，拒绝改写。
func findClosingMovie(text string) (int, error) {
	index := strings.LastIndex(text, "</movie>")
	if index < 0 {
		return 0, errors.New("nfo: 未找到 </movie>，拒绝改写")
	}
	return index, nil
}

// childIndent 取原文顶层子标签的缩进，新插入的标签沿用同一风格（默认 2 空格）。
func childIndent(text string) string {
	for _, line := range strings.Split(text, "\n") {
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

// scalarPattern 匹配 <tag>…</tag>（含带属性）与自闭合 <tag/>。
// 标签名后必须紧跟 >、空白或 /，避免 <title> 误匹配 <titles>。
func scalarPattern(tag string) *regexp.Regexp {
	name := regexp.QuoteMeta(tag)
	return regexp.MustCompile(`(?s)<` + name + `(?:\s[^>]*)?>.*?</` + name + `\s*>|<` + name + `(?:\s[^>]*)?/>`)
}

// hasScalar 判断原文里是否已有该标签且内容非空。
func hasScalar(text, tag string) bool {
	match := scalarPattern(tag).FindString(text)
	if match == "" {
		return false
	}
	if strings.HasSuffix(match, "/>") {
		return false
	}
	open := strings.IndexByte(match, '>')
	close := strings.LastIndex(match, "</")
	if open < 0 || close < 0 || close <= open {
		return false
	}
	return strings.TrimSpace(match[open+1:close]) != ""
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
	if pattern := scalarLinePattern(tag); pattern.MatchString(text) {
		// $1 是原文该行的缩进，沿用原文风格而不是统一改成 2 空格。
		return pattern.ReplaceAllString(text, "$1"+replacement)
	}
	// 标签与其它内容同行（压缩过的 NFO）时上面的行首匹配失效，退回不带缩进的整词替换。
	if pattern := scalarPattern(tag); pattern.MatchString(text) {
		return pattern.ReplaceAllString(text, replacement)
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
	return blockPattern(tag).MatchString(text)
}

// blockPattern 匹配成对标签块（不匹配自闭合，标签块都带子元素）。
func blockPattern(tag string) *regexp.Regexp {
	name := regexp.QuoteMeta(tag)
	return regexp.MustCompile(`(?s)<` + name + `(?:\s[^>]*)?>.*?</` + name + `\s*>`)
}

// replaceBlocks 清空该标签的全部块（连同其所在行），再插入新块。
// rendered 的元素应已含缩进与换行。
func replaceBlocks(text, tag string, rendered []string, indent string) string {
	// 连同行首缩进一起吃掉，避免残留缩进把新块顶偏。
	pattern := regexp.MustCompile(`(?m)^[ \t]*(?s:<` + regexp.QuoteMeta(tag) + `(?:\s[^>]*)?>.*?</` + regexp.QuoteMeta(tag) + `\s*>)[ \t]*\r?\n?`)
	text = pattern.ReplaceAllString(text, "")
	block := strings.Join(rendered, "")
	return insertBeforeClosingMovie(text, strings.TrimRight(block, "\n"), indent)
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
	index := strings.LastIndex(text, closing)
	if index < 0 {
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
	pattern := blockPattern("actor")
	changed := false
	for _, loc := range pattern.FindAllStringIndex(text, -1) {
		block := text[loc[0]:loc[1]]
		if ScrapedTagValue(block, "name") != name {
			continue
		}
		if ScrapedTagValue(block, "thumb") == thumb {
			return false, nil
		}
		replaced := upsertScalarIn(block, "thumb", thumb, actorChildIndent(block), "</actor>")
		text = text[:loc[0]] + replaced + text[loc[1]:]
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
	return uniqueIDPattern(kind).MatchString(text)
}

func uniqueIDPattern(kind string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<uniqueid\s[^>]*type\s*=\s*"` + regexp.QuoteMeta(kind) + `"[^>]*>.*?</uniqueid\s*>`)
}

// upsertUniqueID 按 type 替换 <uniqueid>；同名 type 不存在则新增。
func upsertUniqueID(text, kind, value, indent string) string {
	replacement := indent + `<uniqueid type="` + kind + `">` + escape(value) + "</uniqueid>"
	if pattern := uniqueIDPattern(kind); pattern.MatchString(text) {
		return pattern.ReplaceAllString(text, replacement)
	}
	return insertBeforeClosingMovie(text, replacement, indent)
}

// ScrapedTagValue 读取 NFO 里某个单值标签的当前内容（供「只补缺失」预览与 diff）。
func ScrapedTagValue(text, tag string) string {
	match := scalarPattern(tag).FindString(text)
	if match == "" || strings.HasSuffix(match, "/>") {
		return ""
	}
	open := strings.IndexByte(match, '>')
	close := strings.LastIndex(match, "</")
	if open < 0 || close < 0 || close <= open {
		return ""
	}
	return strings.TrimSpace(match[open+1 : close])
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
		pattern := blockPattern(tag)
		for _, match := range pattern.FindAllString(text, -1) {
			open := strings.IndexByte(match, '>')
			close := strings.LastIndex(match, "</")
			if open < 0 || close < 0 || close <= open {
				continue
			}
			if value := strings.TrimSpace(match[open+1 : close]); value != "" {
				out[tag] = append(out[tag], value)
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
	for _, match := range blockPattern("actor").FindAllString(text, -1) {
		var actor Actor
		if err := xml.Unmarshal([]byte(match), &actor); err != nil {
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
