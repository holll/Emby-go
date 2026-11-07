package scraper

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// 版本标记：ed2k-clean 输出的文件名后缀（-C / -U / -UC），
// 对应 MetaTube 后端内置的三张角标图（见 src/metatube-sdk-go/imageutil/badge）。
//
// 语义取自角标图本身的字样：zimu.png =「中文字幕」、u.png =「无码破解」、uc.png =「中文无码」，
// 与「中文字幕 < 无码 < 中文无码」的常见版本排序一致。

// versionMark 一个版本标记。
type versionMark struct {
	Code  string // 标记本身（大写）
	Tag   string // 写入 NFO <tag> 的文本
	Badge string // MetaTube 图片端点的 badge 参数值
}

// versionMarks 支持的标记。顺序即优先级：长标记在前，
// 否则 -UC 会被 -U 抢先匹配掉。新增标记（如 -ch）在这里加一行即可。
var versionMarks = []versionMark{
	{Code: "UC", Tag: "中文无码", Badge: "uc.png"},
	{Code: "U", Tag: "无码破解", Badge: "u.png"},
	{Code: "C", Tag: "中文字幕", Badge: "zimu.png"},
}

// reVersionMark 匹配文件名末尾的版本标记：串首或分隔符 + 一个独占末段的标记。
//
// 要求标记独占末段，是为了不误判这几类：-CD1 的 1 还在后面所以不匹配；
// LUXU-1234 / 259LUXU-1234 / H4610 末尾不是标记；末尾字母前无分隔符（ABF018C）也不算。
var reVersionMark = regexp.MustCompile(`(?i)(?:^|[-_\s])(UC|U|C)$`)

// subtitleExts 视为外挂字幕的扩展名（小写比较）。
var subtitleExts = []string{".srt", ".ass", ".ssa", ".vtt", ".sub"}

// chineseSubtitleTokens 字幕文件名里表示中文的语言段（小写比较）。
// 只认显式语言段：裸的 movie.srt 语言未知，不当作中文字幕。
var chineseSubtitleTokens = map[string]bool{
	"zh": true, "chi": true, "zho": true, "chs": true, "cht": true,
	"zh-cn": true, "zh-tw": true, "zh-hk": true, "zh-hans": true, "zh-hant": true,
	"chinese": true, "中文": true, "中字": true, "简体": true, "繁体": true, "简中": true, "繁中": true,
}

// hasChineseSubtitle 判断同目录是否有与影片同主名的中文字幕外挂文件。
//
// 只看文件名（主名 + 语言段 + 字幕扩展名），不读字幕内容，也不新增媒体探测：
// 官方那套「按文件名判断字幕」本身就只是启发式，这里不冒称是字幕流检测。
// 目录读不了按「没有」处理——这只是版本标记的附加判断，不能让它挡住刮削。
func hasChineseSubtitle(sourcePath string) bool {
	matches, _ := matchingChineseSubtitles(sourcePath)
	return len(matches) > 0
}

// SubtitleMarkEvidence records filename-only evidence, not subtitle stream detection.
type SubtitleMarkEvidence struct {
	Code    string   `json:"code"`
	Tag     string   `json:"tag"`
	Source  string   `json:"source"`
	Matches []string `json:"matches"`
}

// DetectSubtitleMark prefers explicit filename marks and otherwise lists matching
// Chinese subtitle basenames. An unreadable directory is not evidence of absence.
// Only sourcePath is examined; additional movie parts are not aggregated.
func DetectSubtitleMark(sourcePath string) (SubtitleMarkEvidence, error) {
	evidence := SubtitleMarkEvidence{Source: "none", Matches: []string{}}
	if mark, ok := detectVersionMark(sourcePath); ok {
		evidence.Code, evidence.Tag, evidence.Source = mark.Code, mark.Tag, "filename"
		return evidence, nil
	}
	matches, err := matchingChineseSubtitles(sourcePath)
	if err != nil {
		return evidence, err
	}
	if len(matches) > 0 {
		mark, _ := markByCode("C")
		evidence.Code, evidence.Tag, evidence.Source = mark.Code, mark.Tag, "subtitle"
		evidence.Matches = matches
	}
	return evidence, nil
}

func matchingChineseSubtitles(sourcePath string) ([]string, error) {
	dir := filepath.Dir(sourcePath)
	prefix := strings.ToLower(strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	matches := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !slices.Contains(subtitleExts, filepath.Ext(name)) {
			continue
		}
		// 主名之后、扩展名之前按「.」切段：movie.zh-CN.srt → ["zh-cn"]。
		for _, token := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, prefix), filepath.Ext(name)), ".") {
			if chineseSubtitleTokens[token] {
				matches = append(matches, entry.Name())
				break
			}
		}
	}
	return matches, nil
}

// versionMarkFor 决定本次刮削要写的版本标记。
//
// 文件名后缀优先：那是人工显式标注；文件名没有标记时才按同目录外挂字幕推断中文字幕。
func versionMarkFor(sourcePath string) (versionMark, bool) {
	if mark, ok := detectVersionMark(sourcePath); ok {
		return mark, true
	}
	if !hasChineseSubtitle(sourcePath) {
		return versionMark{}, false
	}
	return markByCode("C")
}

// markByCode 按标记码取表项；表里没有该码时返回 false。
func markByCode(code string) (versionMark, bool) {
	for _, mark := range versionMarks {
		if mark.Code == code {
			return mark, true
		}
	}
	return versionMark{}, false
}

// detectVersionMark 从 .strm 路径的文件名里取出版本标记。
//
// 只看文件名、不看 DB 里的番号：metatube.Trim 的 reSuffix 会把末尾的 -c/-uc 当噪声吃掉
// （-u 恰好不在该正则里才得以保留），所以归一化过的番号已不可靠，原始文件名才是标记的真源。
// 多分段取主段（Split-CD1-U.strm → U）。
func detectVersionMark(sourcePath string) (versionMark, bool) {
	base := filepath.Base(sourcePath)
	// 扩展名超过 7 个字符的当文件名的一部分，不剥（与 metatube.Trim 的判断口径一致）。
	if ext := filepath.Ext(base); len(ext) <= 7 {
		base = strings.TrimSuffix(base, ext)
	}
	match := reVersionMark.FindStringSubmatch(strings.TrimSpace(base))
	if len(match) < 2 {
		return versionMark{}, false
	}
	return markByCode(strings.ToUpper(match[1]))
}
