package server

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"emby-go/internal/metatube"
	"emby-go/internal/nfo"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

// 单条手动刮削的预览数据。
//
// 需求 A7 #49：预览**只显示原文与图片缩略图**（不翻译、不下图），
// 翻译与图片下载都发生在人工确认之后——预览阶段任何写操作都会让「取消」产生副作用。

// scrapeFieldDiff 一个字段的「现有值 → 将写入值」。
type scrapeFieldDiff struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Old    string `json:"old"`
	Next   string `json:"next"`
	Change bool   `json:"change"` // 值会变（覆盖策略下才真正覆盖）
}

// scrapeImageState 一张待写入图片的当前状态。
type scrapeImageState struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Exists  bool   `json:"exists"`
	Preview string `json:"preview,omitempty"`
}

// scrapeInspect 选中候选的详情与 diff。
type scrapeInspect struct {
	Provider string  `json:"provider"`
	ID       string  `json:"id"`
	Number   string  `json:"number"`
	Title    string  `json:"title"`
	Summary  string  `json:"summary"`
	Poster   string  `json:"poster"`
	Backdrop string  `json:"backdrop"`
	Score    float64 `json:"score"`
	Release  string  `json:"release_date,omitempty"`
	Runtime  int     `json:"runtime,omitempty"`
	// Actors 演员姓名（头像由头像任务另取，此处只展示名单）。
	Actors []string `json:"actors"`
	// Fields 字段级 diff；Change 表示该字段在「强制覆盖」下会被改写。
	Fields []scrapeFieldDiff `json:"fields"`
	// Images 会被写入的图片与是否已存在（只补缺失时不覆盖已存在的）。
	Images []scrapeImageState `json:"images"`
	// Overwrite 当前默认覆盖策略，供预览面板初始化开关。
	Overwrite bool `json:"overwrite"`
}

// inspectScalarFields 展示用的单值字段：顺序即界面顺序，tag 与 NFO 标签一致。
var inspectScalarFields = []struct {
	tag   string
	label string
}{
	{"num", "番号"}, {"title", "标题"}, {"originaltitle", "原文标题"}, {"plot", "简介"},
	{"year", "年份"}, {"premiered", "发行日期"}, {"rating", "评分"},
	{"mpaa", "分级"}, {"director", "导演"}, {"maker", "厂商"},
	{"label", "厂牌"}, {"runtime", "时长"},
}

// inspectImageTargets 与 scraper 的落位保持一致（图片类型 → 本库文件名）。
var inspectImageTargets = []struct{ kind, name string }{
	{"primary", "poster.webp"}, {"backdrop", "fanart.webp"}, {"thumb", "landscape.webp"},
}

// buildInspect 组装预览数据：详情字段 + 与现有 NFO 的 diff + 图片落盘状态。
func (a *App) buildInspect(cfg scraper.Config, movie store.Movie, info metatube.MovieInfo) scrapeInspect {
	out := scrapeInspect{
		Provider: info.Provider, ID: info.ID, Number: info.Number,
		Title: strings.TrimSpace(info.Title), Summary: strings.TrimSpace(info.Summary),
		Score: info.Score, Release: strings.TrimSpace(info.ReleaseDate), Runtime: info.Runtime,
		Actors:    nonEmptyList(info.Actors...),
		Poster:    scrapePreviewURL("primary", info.Provider, info.ID),
		Backdrop:  scrapePreviewURL("backdrop", info.Provider, info.ID),
		Overwrite: cfg.Overwrite,
	}

	// 现有值以 NFO 为准（DB 只是索引，手工改过的内容可能只落在 NFO 里）。
	current := map[string]string{}
	if strings.TrimSpace(movie.NFOPath) != "" {
		if values, err := nfo.TagPreview(movie.NFOPath, inspectScalarTags()); err == nil {
			current = values
		}
	}
	next := inspectNextValues(info)
	for _, item := range inspectScalarFields {
		out.Fields = append(out.Fields, scrapeFieldDiff{
			Key: item.tag, Label: item.label, Old: current[item.tag], Next: next[item.tag],
			Change: next[item.tag] != "" && next[item.tag] != current[item.tag],
		})
	}
	// 列表字段单独成行；这些标签整类重建，无法逐项 diff，只展示将写入的内容。
	for _, item := range []struct {
		tag   string
		label string
		value []string
	}{
		{"genre", "类型", nonEmptyList(info.Genres...)},
		{"studio", "厂商", nonEmptyList(info.Maker)},
		{"actor", "演员", nonEmptyList(info.Actors...)},
	} {
		if len(item.value) == 0 {
			continue
		}
		out.Fields = append(out.Fields, scrapeFieldDiff{
			Key: item.tag, Label: item.label, Next: strings.Join(item.value, " / "), Change: true,
		})
	}

	dir := strings.TrimSpace(movie.OutputDir)
	if dir == "" {
		dir = filepath.Dir(movie.SourcePath)
	}
	for _, target := range inspectImageTargets {
		state := scrapeImageState{
			Name: target.name, Kind: target.kind,
			Preview: scrapePreviewURL(target.kind, info.Provider, info.ID),
		}
		if stat, err := os.Stat(filepath.Join(dir, target.name)); err == nil && stat.Size() > 0 {
			state.Exists = true
		}
		out.Images = append(out.Images, state)
	}
	return out
}

func inspectScalarTags() []string {
	out := make([]string, 0, len(inspectScalarFields))
	for _, item := range inspectScalarFields {
		out = append(out, item.tag)
	}
	return out
}

// inspectNextValues 把详情映射成「标签 → 将写入值」。
// 口径与 scraper.buildFields 保持一致：标题取原文（预览不翻译），原文另落 originaltitle。
func inspectNextValues(info metatube.MovieInfo) map[string]string {
	premiered, year := splitDate(info.ReleaseDate)
	title := strings.TrimSpace(info.Title)
	return map[string]string{
		"num":           strings.TrimSpace(info.Number),
		"title":         title,
		"originaltitle": title,
		"plot":          strings.TrimSpace(info.Summary),
		"year":          positiveIntText(year),
		"premiered":     premiered,
		"rating":        positiveFloatText(info.Score),
		"mpaa":          "JP-18+",
		"director":      strings.TrimSpace(info.Director),
		"maker":         strings.TrimSpace(info.Maker),
		"label":         strings.TrimSpace(info.Label),
		"runtime":       positiveIntText(info.Runtime),
	}
}

// splitDate 取日期部分与年份；解析不出来就都留空（与 scraper 的口径一致）。
func splitDate(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if index := strings.IndexAny(raw, "T "); index > 0 {
		raw = raw[:index]
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return "", 0
	}
	return parsed.Format("2006-01-02"), parsed.Year()
}

func positiveIntText(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func positiveFloatText(value float64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatFloat(value, 'f', 1, 64)
}

// scrapePreviewURL 生成走本服务代理的缩略图地址（不落盘，只转发）。
func scrapePreviewURL(kind, provider, id string) string {
	if provider == "" || id == "" {
		return ""
	}
	return "/api/admin/scrape/image?kind=" + kind + "&provider=" + provider + "&id=" + id
}

func nonEmptyList(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
