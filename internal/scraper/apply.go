package scraper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"emby-go/internal/imageutil"
	"emby-go/internal/metatube"
	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

// officialRating 与官方 Jellyfin 插件保持一致：AV 类内容统一分级。
const officialRating = "JP-18+"

// maxImageBytes 单张图片的下载上限，避免上游返回异常大文件把内存打满。
const maxImageBytes = 32 << 20

// 图片落位（需求 5c 步骤 7）：MetaTube 的图片类型 → 本库惯用文件名。
var imageTargets = []struct {
	kind string
	name string
}{
	{"primary", "poster.webp"},
	{"backdrop", "fanart.webp"},
	{"thumb", "landscape.webp"},
}

// ApplyOptions 一次落盘的参数。
type ApplyOptions struct {
	Provider  string
	ID        string
	Overwrite bool
	// Refresh 为真时写入后立即单文件重扫该片（单条手动路径用）；
	// 批量/定时路径传 false，由调用方在全部结束后整库扫一次。
	Refresh bool
}

// ApplyResult 一次刮削的结果。
type ApplyResult struct {
	MovieID    int64
	Provider   string
	ProviderID string
	Number     string
	Title      string
	Translated bool
	Images     []string // 落盘的图片路径（供上层失效 tag 缓存）
	ImageCount int
	ImageWarn  []string
	// RefreshError 记录「元数据已写入但索引刷新失败」，供上层提示（不算整体失败）。
	RefreshError string
}

// MovieInfo 取详情（单条手动确认后按 provider:id 重新取，预览不落中间态）。
func (s *Scraper) MovieInfo(ctx context.Context, provider, id string) (metatube.MovieInfo, error) {
	return s.client.MovieInfo(ctx, provider, id, true)
}

// Apply 把指定候选写入 NFO 与图片。
//
// 顺序刻意是「先图片后 NFO」：图片尽力而为（失败只记 warn），
// 元数据成功即算成功；若先写 NFO 再下图，中途失败会留下「元数据已更新但图没换」的
// 半成品且无法从 NFO 判断，反过来则最多是图旧了一点。
func (s *Scraper) Apply(ctx context.Context, movie store.Movie, info metatube.MovieInfo, opts ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{MovieID: movie.ID, Provider: info.Provider, ProviderID: providerID(info.Provider, info.ID)}
	result.Number = info.Number

	title, plot, translated := s.translateFields(ctx, info)
	result.Title, result.Translated = title, translated

	// 图片落盘（只补缺失时不覆盖已有文件）。
	dir := strings.TrimSpace(movie.OutputDir)
	if dir == "" {
		dir = filepath.Dir(movie.SourcePath)
	}
	if s.cfg.DownloadImages {
		for _, target := range imageTargets {
			path, err := s.downloadImage(ctx, info, target.kind, filepath.Join(dir, target.name), opts.Overwrite)
			if err != nil {
				// 图片尽力而为：元数据成功即算成功（需求 A5 #28）。
				slog.Warn("刮削下载图片失败", "movie_id", movie.ID, "kind", target.kind, "error", err)
				result.ImageWarn = append(result.ImageWarn, target.kind+": "+err.Error())
				continue
			}
			if path != "" {
				result.Images = append(result.Images, path)
				result.ImageCount++
			}
		}
	}

	fields := buildFields(info, title, plot, opts.Overwrite)
	if err := writeNFO(movie, fields); err != nil {
		return result, err
	}
	if s.store != nil {
		// 记录本次刮削结果；成功时清空上次的错误/待确认标记。
		if err := s.store.SetScrapeResult(movie.ID, ""); err != nil {
			slog.Warn("写入刮削结果失败", "movie_id", movie.ID, "error", err)
		}
	}
	// 单条路径：写完立即单文件重扫，让元数据马上对 API 生效。
	// 批量路径不在这里扫（逐部重扫是 O(n²)），由调用方在全部结束后整库扫一次。
	if opts.Refresh && s.store != nil {
		if err := s.rescan(movie); err != nil {
			// 重扫失败不算刮削失败：NFO 已落盘，下次扫库会把元数据带进索引。
			slog.Warn("刮削后单文件重扫失败", "movie_id", movie.ID, "error", err)
			result.RefreshError = err.Error()
		}
	}
	return result, nil
}

// translateFields 按开关翻译标题与简介。任一失败都回退原文并记日志，不阻断刮削（需求 A5 #31）。
func (s *Scraper) translateFields(ctx context.Context, info metatube.MovieInfo) (title, plot string, translated bool) {
	title = strings.TrimSpace(info.Title)
	plot = strings.TrimSpace(info.Summary)
	if !s.cfg.Translate.Enabled() {
		return title, plot, false
	}
	// 只翻开启的字段；两个都开时合成一次请求，省一次往返与一份配额。
	texts := make([]string, 0, 2)
	kinds := make([]string, 0, 2)
	if s.cfg.TranslateTitle && title != "" {
		texts = append(texts, title)
		kinds = append(kinds, "title")
	}
	if s.cfg.TranslatePlot && plot != "" {
		texts = append(texts, plot)
		kinds = append(kinds, "plot")
	}
	if len(texts) == 0 {
		return title, plot, false
	}
	response, err := s.tr.Translate(ctx, texts)
	if err != nil {
		slog.Warn("刮削翻译失败，已回退原文", "provider", info.Provider, "id", info.ID, "error", err)
		return title, plot, false
	}
	for i, kind := range kinds {
		if i >= len(response.Texts) {
			break
		}
		switch kind {
		case "title":
			title = response.Texts[i]
		case "plot":
			plot = response.Texts[i]
		}
	}
	slog.Debug("刮削翻译完成", "provider", info.Provider, "id", info.ID,
		"target_lang", s.tr.TargetLang(), "cache", response.CacheStatus, "detected", response.DetectedSrc)
	return title, plot, true
}

// downloadImage 下载一张图并转 webp 落盘；只补缺失模式下已有文件则跳过（返回空路径）。
func (s *Scraper) downloadImage(ctx context.Context, info metatube.MovieInfo, kind, dest string, overwrite bool) (string, error) {
	if !overwrite {
		if stat, err := os.Stat(dest); err == nil && !stat.IsDir() && stat.Size() > 0 {
			return "", nil
		}
	}
	// 不传 url 参数：后端按 provider:id 自己取源图并按类型裁到约定比例。
	// 某些来源没有某类图时后端会返回错误，我们记 warn 跳过（图片尽力而为）。
	url := s.client.ImageURL(kind, info.Provider, info.ID, s.cfg.ImageQualityValue(), "")
	data, err := s.client.Download(ctx, url, maxImageBytes)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("图片内容为空")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	if err := imageutil.EncodeWebP(bytes.NewReader(data), tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("转 webp 失败: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dest, nil
}

// providerID 三段式 ProviderID，用于去重与回查（需求映射表）。
func providerID(provider, id string) string {
	return "metatube:" + provider + ":" + id
}

// SplitProviderID 解析 metatube:<provider>:<id>。
func SplitProviderID(raw string) (provider, id string, ok bool) {
	if !strings.HasPrefix(raw, "metatube:") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(raw, "metatube:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// buildFields 把详情映射为 NFO 字段（映射表见需求 4.4）。
func buildFields(info metatube.MovieInfo, title, plot string, overwrite bool) nfo.ScrapeFields {
	premiered, year := splitReleaseDate(info.ReleaseDate)
	// 映射表：genres → Genres（不翻译）、label → Label + Tags。
	// 刻意不把 genres 也塞进 tags：Emby 里两者是不同维度，重复写入会让客户端
	// 的类型/标签筛选出现同一批重复项。
	tags := []string{}
	if label := strings.TrimSpace(info.Label); label != "" {
		tags = append(tags, label)
	}
	studios := []string{}
	if maker := strings.TrimSpace(info.Maker); maker != "" {
		studios = append(studios, maker)
	}
	actors := make([]nfo.Actor, 0, len(info.Actors))
	for _, name := range info.Actors {
		if name = strings.TrimSpace(name); name != "" {
			actors = append(actors, nfo.Actor{Name: name, Type: "Actor"})
		}
	}
	runtime := info.Runtime
	if runtime <= 0 {
		runtime = 0
	}
	return nfo.ScrapeFields{
		Number:        strings.TrimSpace(info.Number),
		Title:         title,
		OriginalTitle: strings.TrimSpace(info.Title),
		SortTitle:     strings.TrimSpace(info.Title),
		Plot:          plot,
		Year:          year,
		Premiered:     premiered,
		Rating:        info.Score,
		Mpaa:          officialRating,
		Director:      strings.TrimSpace(info.Director),
		Maker:         strings.TrimSpace(info.Maker),
		Label:         strings.TrimSpace(info.Label),
		Runtime:       runtime,
		Series:        strings.TrimSpace(info.Series),
		ProviderID:    providerID(info.Provider, info.ID),
		Genres:        info.Genres,
		Tags:          tags,
		Studios:       studios,
		Actors:        actors,
		Overwrite:     overwrite,
	}
}

// splitReleaseDate 把 YYYY-MM-DD 拆成 premiered 与年份；解析不出来就都留空。
func splitReleaseDate(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	// 上游可能给完整时间戳，只取日期部分。
	if index := strings.IndexAny(raw, "T "); index > 0 {
		raw = raw[:index]
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return "", 0
	}
	return parsed.Format("2006-01-02"), parsed.Year()
}

// writeNFO 走标签级更新器写盘。NFO 位置以影片已登记的 NFOPath 为准；
// 没有时按 scanner 的约定（与 .strm 同名的 .nfo）新建。
func writeNFO(movie store.Movie, fields nfo.ScrapeFields) error {
	path := strings.TrimSpace(movie.NFOPath)
	if path == "" {
		path = strings.TrimSuffix(movie.SourcePath, filepath.Ext(movie.SourcePath)) + ".nfo"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return nfo.UpdateScraped(path, fields)
}
