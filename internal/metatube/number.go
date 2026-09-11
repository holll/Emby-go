package metatube

import (
	"path"
	"regexp"
	"strings"
)

// 番号归一化。
//
// 本文件移植自 MetaTube SDK 的 common/number（Apache License 2.0，
// https://github.com/metatube-community/metatube-sdk-go），按原逻辑逐条保留：
// 文件名里混着分辨率、来源站名、版本后缀等噪声，只有归一化后才能做「番号精确命中」
// 的判断（对应需求 A5 #27 的搜索选结果策略）。
//
// 归一化结果只用于搜索与比对，不写回库——库里存的仍是 NFO 里的原始番号。

// Trim 从文件名/标题里提取归一化番号。取不到时返回空串或原文的裁剪结果。
func Trim(s string) string {
	const maxExtLength = 7
	if ext := path.Ext(s); len(ext) < maxExtLength {
		s = s[:len(s)-len(ext)] // trim extension
	}
	s = reTrimDomain.ReplaceAllString(s, "") // trim domain
	if ss := reDashedNumber.FindStringSubmatch(s); len(ss) > 0 {
		s = ss[1] // first find number with dashes
	} else if ss = reAlphaDigit.FindStringSubmatch(s); len(ss) > 1 {
		s = ss[1] // otherwise find number with alphas & digits
	}
	s = reSpecialPrefix.ReplaceAllString(s, "${1}") // trim special prefixes
	s = reTags.ReplaceAllString(s, "")              // trim tags
	s = reMakers.ReplaceAllString(s, "${pattern}")  // trim makers
	s = reFC2Prefix.ReplaceAllString(s, "FC2-")     // normalize fc2 prefixes
	for reSuffix.MatchString(s) {
		s = reSuffix.ReplaceAllString(s, "") // repeatedly trim suffixes
	}
	return strings.TrimSpace(s)
}

var (
	reTrimDomain    = regexp.MustCompile(`(?i)([a-z\d]+\.(?:com|net|top|xyz|tv))(?:[^a-z\d]|$)`)
	reDashedNumber  = regexp.MustCompile(`(?i)([a-z\d]+(?:[-_][a-z\d]{2,})+)`)
	reAlphaDigit    = regexp.MustCompile(`(?i)((?:[a-z]+\d|\d+[a-z])[a-z\d]+)`)
	reSpecialPrefix = regexp.MustCompile(`(?i)^(?:f?hd|sd)[-_](.*$)`)
	reTags          = regexp.MustCompile(`(?i)[-_.](dvd|iso|mkv|mp4|c?avi|\d*fps|whole|(f|hhb)?hd\d*|sd\d*|(?:360|480|720|1080|2160)[pi]|X1080X|uncensored|leak|[2468]ks?|[xh]26[45])+`)
	reMakers        = regexp.MustCompile(`(?i)(^|[-_\s]+)(carib(b?ean)?(com)?(pr)?|1?Pond?o?|10mu(sume)?|paco(paco)?(mama)?|mura(mura)?|Tokyo[-_\s]?Hot)([-_\s]+(?P<pattern>\d{4,}[-_]\d{2,}|[a-z]{1,4}\d{2,4})|$)`)
	reFC2Prefix     = regexp.MustCompile(`^(?i)\s*(FC2[-_]?PPV)[-_]`)
	reSuffix        = regexp.MustCompile(`(?i)([-_](c|uc|ch|cd\d{1,2})|hhb\d*|ch|A|B|C|D)\s*$`)
)

// Normalize 把番号归一化为比较用的形式：去空白、大写、统一分隔符。
// 例：abf 018 / ABF_018 / ABF-018-c → ABF-018
func Normalize(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	// 压缩重复分隔符
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// SameNumber 判断两个番号是否指同一部片。
// 空串一律不同（避免「都没番号」被判为命中）。
func SameNumber(a, b string) bool {
	left, right := Normalize(a), Normalize(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	// 去掉分隔符再比一次：ABF018 与 ABF-018 视为同一部。
	return compact(left) == compact(right)
}

func compact(s string) string {
	var builder strings.Builder
	builder.Grow(len(s))
	for _, char := range s {
		if char != '-' && char != '.' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}
