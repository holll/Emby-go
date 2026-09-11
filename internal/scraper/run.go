package scraper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
)

// Progress 批量刮削的进度快照（调用方负责节流展示）。
type Progress struct {
	Total       int
	Done        int
	Success     int
	Skipped     int
	Failed      int
	Consecutive int // 当前连续失败数
	Current     string
}

// ItemResult 单部影片的刮削结果。
type ItemResult struct {
	MovieID  int64
	Title    string
	Status   string // success / skipped / failed
	Reason   string
	Provider string
	Number   string
	Images   []string
}

// 单条结果的三种状态。
const (
	StatusSuccess = "success"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// RunOptions 批量/定时刮削的参数。
type RunOptions struct {
	LibraryID   int64
	OnlyMissing bool
	Limit       int
	Overwrite   bool
	// RefreshPerMovie 为真时每部写完立即单文件重扫（单条路径）；批量为假，结束后整库扫一次。
	RefreshPerMovie bool
	// OnProgress 每次处理完一部回调一次（可为 nil）。
	OnProgress func(Progress)
	// OnItem 每部处理完回调（可为 nil），供调用方收集失败原因与失效图片缓存。
	OnItem func(ItemResult)
}

// Run 批量刮削：并发处理候选影片，带熔断与进度回调。
//
// 返回的 error 只表示「整批无法继续」（环境性故障/上下文取消）；
// 单部的失败记在 ItemResult 里，不影响其余影片。
func (s *Scraper) Run(ctx context.Context, opts RunOptions) (Progress, error) {
	if !s.cfg.Configured() {
		return Progress{}, errors.New("未配置 MetaTube 地址或 token")
	}
	if s.store == nil {
		return Progress{}, errors.New("缺少存储，无法读取刮削候选")
	}
	movies, err := s.store.MoviesForScrape(opts.LibraryID, opts.OnlyMissing, opts.Limit)
	if err != nil {
		return Progress{}, err
	}
	return s.runMovies(ctx, movies, opts)
}

// RunMovies 对给定影片列表执行刮削（单条手动路径复用同一套并发与熔断逻辑）。
func (s *Scraper) RunMovies(ctx context.Context, movies []store.Movie, opts RunOptions) (Progress, error) {
	if !s.cfg.Configured() {
		return Progress{}, errors.New("未配置 MetaTube 地址或 token")
	}
	return s.runMovies(ctx, movies, opts)
}

func (s *Scraper) runMovies(ctx context.Context, movies []store.Movie, opts RunOptions) (Progress, error) {
	progress := Progress{Total: len(movies)}
	if len(movies) == 0 {
		return progress, nil
	}

	var (
		mu      sync.Mutex
		aborted atomic.Bool
		total   atomic.Int64
	)
	report := func(update func(*Progress)) {
		mu.Lock()
		update(&progress)
		snapshot := progress
		mu.Unlock()
		if opts.OnProgress != nil {
			opts.OnProgress(snapshot)
		}
	}

	emit := func(result ItemResult) {
		total.Add(1)
		other := total.Load()
		report(func(p *Progress) {
			p.Done = int(other)
			p.Current = result.Title
			switch result.Status {
			case StatusSuccess:
				p.Success++
				p.Consecutive = 0
			case StatusSkipped:
				p.Skipped++
				// 跳过不是故障，不累加连续失败。
				p.Consecutive = 0
			default:
				p.Failed++
				p.Consecutive++
			}
		})
		if opts.OnItem != nil {
			opts.OnItem(result)
		}
	}

	jobs := make(chan store.Movie)
	workers := s.cfg.Workers()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 熔断后不直接退出：派发方可能还在阻塞投递，排空 channel 才不会死锁
			//（与探测任务的处理方式一致）。
			for movie := range jobs {
				if ctx.Err() != nil || aborted.Load() {
					continue
				}
				result := s.scrapeOne(ctx, movie, opts)
				emit(result)
				if result.Status == StatusFailed {
					mu.Lock()
					consecutive := progress.Consecutive
					mu.Unlock()
					if consecutive >= MaxConsecutiveFailures {
						// 连续失败到阈值：判定为环境性故障（MetaTube 不可达 / token 失效），
						// 继续跑只会把剩余影片全部标记为失败。
						if aborted.CompareAndSwap(false, true) {
							slog.Warn("刮削触发熔断：连续失败过多，中止本次任务",
								"consecutive", consecutive, "done", total.Load(), "total", len(movies))
						}
					}
				}
			}
		}()
	}
	for _, movie := range movies {
		if ctx.Err() != nil || aborted.Load() {
			break
		}
		jobs <- movie
	}
	close(jobs)
	wg.Wait()

	if aborted.Load() {
		return progress, fmt.Errorf("连续 %d 条刮削失败，疑似 MetaTube 不可达或 token 失效，任务已提前中止（已处理 %d/%d）",
			MaxConsecutiveFailures, progress.Done, len(movies))
	}
	if err := ctx.Err(); err != nil {
		return progress, err
	}
	return progress, nil
}

// scrapeOne 处理一部影片：搜索 → 选结果 → 取详情 → 落盘。
func (s *Scraper) scrapeOne(ctx context.Context, movie store.Movie, opts RunOptions) ItemResult {
	result := ItemResult{MovieID: movie.ID, Title: displayTitle(movie)}

	query := searchQuery(movie)
	if query == "" {
		return s.recordSkip(movie, result, "无法构造搜索词：番号、文件名与标题都为空")
	}
	expected := expectedNumber(movie)

	results, err := s.client.SearchMovies(ctx, query, "", true)
	if err != nil {
		return s.recordFailure(movie, result, "搜索失败: "+err.Error())
	}
	if len(results) == 0 {
		return s.recordSkip(movie, result, confirmMessage("搜索无结果（query="+query+"）"))
	}
	chosen, exact := selectExact(results, expected)
	if !exact {
		// 批量/定时路径不做「退回首个」：那会把错误影片的元数据写进这部片（需求 A7 #50）。
		return s.recordSkip(movie, result,
			confirmMessage(fmt.Sprintf("非番号精确命中（query=%s，期望=%s，首条=%s/%s）",
				query, expected, results[0].Provider, results[0].Number)))
	}
	logScrapeChoice(movie, query, expected, chosen, exact)
	result.Provider, result.Number = chosen.Provider, chosen.Number

	info, err := s.client.MovieInfo(ctx, chosen.Provider, chosen.ID, true)
	if err != nil {
		return s.recordFailure(movie, result, "取详情失败: "+err.Error())
	}
	applied, err := s.Apply(ctx, movie, info, ApplyOptions{
		Provider: chosen.Provider, ID: chosen.ID, Overwrite: opts.Overwrite, Refresh: opts.RefreshPerMovie,
	})
	if err != nil {
		return s.recordFailure(movie, result, "写入失败: "+err.Error())
	}
	result.Status = StatusSuccess
	result.Images = applied.Images
	return result
}

// recordSkip 记「跳过」：批量路径下这类条目需要人工确认，写进 last_scrape_error。
func (s *Scraper) recordSkip(movie store.Movie, result ItemResult, reason string) ItemResult {
	result.Status, result.Reason = StatusSkipped, reason
	slog.Info("刮削跳过", "movie_id", movie.ID, "title", movie.Title, "reason", reason)
	s.storeScrapeResult(movie, reason)
	return result
}

// recordFailure 记「失败」：失败原因同样落在 last_scrape_error，供管理端筛出。
func (s *Scraper) recordFailure(movie store.Movie, result ItemResult, reason string) ItemResult {
	result.Status, result.Reason = StatusFailed, reason
	slog.Warn("刮削失败", "movie_id", movie.ID, "title", movie.Title, "reason", reason)
	s.storeScrapeResult(movie, reason)
	return result
}

func (s *Scraper) storeScrapeResult(movie store.Movie, message string) {
	if s.store == nil {
		return
	}
	if err := s.store.SetScrapeResult(movie.ID, message); err != nil {
		slog.Warn("写入刮削结果失败", "movie_id", movie.ID, "error", err)
	}
}

// confirmMessage 给「待人工确认」加统一前缀，管理端据此与真实失败区分开。
func confirmMessage(reason string) string {
	return store.ScrapeConfirmPrefix + reason
}

// ScrapeOneMovie 单条手动刮削：直接按指定候选写入（预览已由人工确认），
// 写完立即单文件重扫刷新索引，并回报落盘的图片路径。
func (s *Scraper) ScrapeOneMovie(ctx context.Context, movie store.Movie, provider, id string, overwrite bool) (ApplyResult, error) {
	info, err := s.client.MovieInfo(ctx, provider, id, true)
	if err != nil {
		return ApplyResult{}, err
	}
	applied, err := s.Apply(ctx, movie, info, ApplyOptions{Provider: provider, ID: id, Overwrite: overwrite, Refresh: true})
	if err != nil {
		return applied, err
	}
	return applied, nil
}

// displayTitle 取用于日志/进度的展示名。
func displayTitle(movie store.Movie) string {
	if title := strings.TrimSpace(movie.Title); title != "" {
		return title
	}
	if number := strings.TrimSpace(movie.Number); number != "" {
		return number
	}
	return movie.SourcePath
}

// IsNotFound 供上层区分「上游没有这部片」与真正的故障。
func IsNotFound(err error) bool { return metatube.IsNotFound(err) }
