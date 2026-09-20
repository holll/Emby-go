package scraper

import (
	"path/filepath"
	"regexp"
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
	code := strings.ToUpper(match[1])
	for _, mark := range versionMarks {
		if mark.Code == code {
			return mark, true
		}
	}
	return versionMark{}, false
}
