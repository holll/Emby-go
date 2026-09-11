// Package scraper 编排刮削：搜索 → 选结果 → 取详情 → 翻译 → 下载图片 → 写 NFO。
//
// 职责边界：
//   - 本包不认识 HTTP/管理端，进度与取消靠调用方传入的回调与 context；
//   - NFO 是唯一元数据真源，本包只通过 internal/nfo 的标签级更新器写盘，
//     绝不整体重写（会丢外部工具写的扩展标签）；
//   - 索引刷新由调用方决定：单条走单文件重扫，批量结束时整库扫一次。
package scraper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
	"emby-go/internal/translate"
)

// MaxConsecutiveFailures 连续失败达到该数量即判定环境性故障并中止整批刮削。
// 取值与媒体信息探测任务对齐（server.maxProbeConsecutiveFailures）：单个失效源不会连续失败这么多次，
// 而 MetaTube 不可达/401 这类问题会瞬间连续失败成百上千条，必须尽早中止。
const MaxConsecutiveFailures = 20

// Config 刮削配置。与需求 A6 #43 的配置项一一对应，默认值见 DefaultConfig。
type Config struct {
	BaseURL        string
	Token          string
	TimeoutSeconds int
	Concurrency    int
	DownloadImages bool
	ImageQuality   int
	AvatarsDir     string
	Overwrite      bool // 覆盖策略：false=只补缺失（默认）

	TranslateTitle bool
	TranslatePlot  bool
	Translate      translate.Config
}

// 默认值：DB 只存用户改过的项，缺省走这里（需求 5h）。
const (
	defaultTimeoutSeconds = 30
	defaultConcurrency    = 2
	defaultImageQuality   = 90
	defaultTranslateLang  = "ZH"
)

// DefaultConfig 返回内置默认配置（不含任何密钥）。
func DefaultConfig() Config {
	return Config{
		TimeoutSeconds: defaultTimeoutSeconds,
		Concurrency:    defaultConcurrency,
		DownloadImages: true,
		ImageQuality:   defaultImageQuality,
		TranslateTitle: true,
		TranslatePlot:  true,
		Translate: translate.Config{
			TargetLang: defaultTranslateLang,
			Timeout:    translate.DefaultTimeout,
		},
	}
}

// Timeout 生效的请求超时。
func (c Config) Timeout() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return defaultTimeoutSeconds * time.Second
}

// Workers 生效的影片并发数（上限 8：再高只会互相拖慢，上游也有速率限制）。
func (c Config) Workers() int {
	if c.Concurrency <= 0 {
		return defaultConcurrency
	}
	if c.Concurrency > 8 {
		return 8
	}
	return c.Concurrency
}

// ImageQualityValue 生效的图片质量。
func (c Config) ImageQualityValue() int {
	if c.ImageQuality > 0 && c.ImageQuality <= 100 {
		return c.ImageQuality
	}
	return defaultImageQuality
}

// Scraper 一次运行期内的刮削器。
type Scraper struct {
	cfg    Config
	client *metatube.Client
	tr     *translate.Client
	store  *store.Store
}

// New 创建刮削器。store 用于读取候选与写入刮削结果，nil 时只做「无状态」操作（预览）。
func New(cfg Config, st *store.Store) *Scraper {
	return &Scraper{
		cfg:    cfg,
		client: metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout()),
		tr:     translate.New(cfg.Translate),
		store:  st,
	}
}

// Configured 判断是否具备刮削条件（缺地址或 token 时调用方应给出明确提示）。
func (c Config) Configured() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Token) != ""
}

// Client 暴露底层客户端，供管理端「测试连接」使用。
func (s *Scraper) Client() *metatube.Client { return s.client }

// Translator 暴露翻译客户端，供管理端「测试连接」使用。
func (s *Scraper) Translator() *translate.Client { return s.tr }

// Candidate 一个搜索候选（单条手动预览用）。
type Candidate struct {
	Provider string  `json:"provider"`
	ID       string  `json:"id"`
	Number   string  `json:"number"`
	Title    string  `json:"title"`
	Score    float64 `json:"score"`
	Release  string  `json:"release_date,omitempty"`
	// Thumb 是经本服务代理的缩略图地址：MetaTube 常在内网/HTTP，
	// 直接给客户端地址会被混合内容拦截或根本不可达。
	Thumb string `json:"thumb,omitempty"`
	Exact bool   `json:"exact"` // 番号是否与预期精确一致
}

// PreviewResult 单条手动刮削的预览：候选列表 + 推荐项。不翻译、不下图、不写盘。
type PreviewResult struct {
	MovieID     int64       `json:"movie_id"`
	Query       string      `json:"query"`
	Expected    string      `json:"expected_number,omitempty"`
	Candidates  []Candidate `json:"candidates"`
	Recommended int         `json:"recommended"` // 推荐候选下标；-1 表示无
	Exact       bool        `json:"exact"`       // 是否有番号精确命中
}

// searchQuery 构造搜索词：DB 番号 → 文件名番号（与后端同一套归一化）→ 标题。
//
// 番号优先是因为标题常带修饰词、搜出来一堆相似结果；文件名兜底是为了兼容
// 尚未入库番号的手工文件（如 ABF-018.mp4.strm）。
func searchQuery(m store.Movie) string {
	if number := strings.TrimSpace(m.Number); number != "" {
		return number
	}
	base := strings.TrimSuffix(filepath.Base(m.SourcePath), filepath.Ext(m.SourcePath))
	if number := strings.TrimSpace(metatube.Trim(base)); number != "" {
		return number
	}
	return strings.TrimSpace(m.Title)
}

// expectedNumber 返回用于「番号精确命中」比对的番号（取不到时为空 → 一律视为非精确）。
func expectedNumber(m store.Movie) string {
	if number := metatube.Normalize(m.Number); number != "" {
		return number
	}
	base := strings.TrimSuffix(filepath.Base(m.SourcePath), filepath.Ext(m.SourcePath))
	return metatube.Normalize(metatube.Trim(base))
}

// candidateLimit 单条手动预览最多展示的候选数（需求 A7 #51：前 5 条）。
const candidateLimit = 5

// Preview 搜索候选供人工确认。全程无副作用（不翻译、不下载、不写盘）。
func (s *Scraper) Preview(ctx context.Context, m store.Movie) (PreviewResult, error) {
	result := PreviewResult{MovieID: m.ID, Recommended: -1}
	query := searchQuery(m)
	if query == "" {
		return result, errors.New("无法构造搜索词：番号、文件名与标题都为空")
	}
	result.Query = query
	result.Expected = expectedNumber(m)

	results, err := s.client.SearchMovies(ctx, query, "", true)
	if err != nil {
		return result, err
	}
	results = pickCandidates(results, result.Expected, candidateLimit)
	for index, item := range results {
		exact := result.Expected != "" && metatube.SameNumber(item.Number, result.Expected)
		if exact && result.Recommended < 0 {
			result.Recommended = index
			result.Exact = true
		}
		result.Candidates = append(result.Candidates, Candidate{
			Provider: item.Provider, ID: item.ID, Number: item.Number, Title: item.Title,
			Score: item.Score, Release: item.ReleaseDate,
			Thumb: proxyImageURL("thumb", item.Provider, item.ID),
			Exact: exact,
		})
	}
	// 没有精确命中时默认选第一个，由人工判断（需求 5j）。
	if result.Recommended < 0 && len(result.Candidates) > 0 {
		result.Recommended = 0
	}
	return result, nil
}

// pickCandidates 优先展示番号精确命中的候选，其余按原顺序补齐到 limit 条。
func pickCandidates(results []metatube.MovieSearchResult, expected string, limit int) []metatube.MovieSearchResult {
	if len(results) <= limit {
		return results
	}
	out := make([]metatube.MovieSearchResult, 0, limit)
	rest := make([]metatube.MovieSearchResult, 0, len(results))
	for _, item := range results {
		if expected != "" && metatube.SameNumber(item.Number, expected) {
			out = append(out, item)
			continue
		}
		rest = append(rest, item)
	}
	for _, item := range rest {
		if len(out) >= limit {
			break
		}
		out = append(out, item)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// selectExact 在搜索结果里挑出番号精确命中的那条。
// 批量/定时路径只能走这个函数：没有精确命中就跳过（需求 A7 #50），不退回首个结果。
func selectExact(results []metatube.MovieSearchResult, expected string) (metatube.MovieSearchResult, bool) {
	if expected == "" {
		return metatube.MovieSearchResult{}, false
	}
	for _, item := range results {
		if metatube.SameNumber(item.Number, expected) {
			return item, true
		}
	}
	return metatube.MovieSearchResult{}, false
}

// proxyImageURL 生成指向本服务的图片代理地址（前端预览与落盘都不用直连 MetaTube）。
func proxyImageURL(kind, provider, id string) string {
	if provider == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("/api/admin/scrape/image?kind=%s&provider=%s&id=%s", kind, provider, id)
}

// logScrapeChoice 把「选了哪条、相似度多少、是否番号精确命中」记进日志（验收项要求）。
func logScrapeChoice(movie store.Movie, query, expected string, chosen metatube.MovieSearchResult, exact bool) {
	slog.Info("刮削选中结果", "movie_id", movie.ID, "title", movie.Title,
		"query", query, "expected_number", expected, "provider", chosen.Provider,
		"selected_number", chosen.Number, "selected_title", chosen.Title,
		"score", chosen.Score, "exact_number", exact)
}
