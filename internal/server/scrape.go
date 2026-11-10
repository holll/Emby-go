package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/metatube"
	"emby-go/internal/scanner"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

// 刮削任务：搜索 MetaTube → 写 NFO + 图片 → 刷新索引。
//
// 与探测/扫库三者互斥（见 claimNFO）：三者都会「读全文 → 改局部 → 原子写」同一批 NFO，
// 并发会互相覆盖；刮削与整库扫描并行时 DeleteMissingSources 还可能误删。
// scrape 与 scrape_avatars 写的是不同数据（NFO 元数据 vs actors 表 + thumb 标签），
// 但头像任务也会改 NFO 的 <actor><thumb>，故同样纳入互斥，避免与影片刮削抢同一个文件。

// scrapeStatus 刮削任务的进度快照，供管理端轮询。
type scrapeStatus struct {
	Running            bool     `json:"running"`
	Kind               string   `json:"kind"` // scrape / scrape_avatars
	Total              int      `json:"total"`
	Done               int      `json:"done"`
	Success            int      `json:"success"`
	Skipped            int      `json:"skipped"`
	Failed             int      `json:"failed"`
	Current            string   `json:"current"`
	StartedAt          string   `json:"started_at,omitempty"`
	FinishedAt         string   `json:"finished_at,omitempty"`
	Error              string   `json:"error,omitempty"`
	Cancelled          bool     `json:"cancelled"`
	IndexRefreshFailed bool     `json:"index_refresh_failed,omitempty"`
	Failures           []string `json:"failures,omitempty"` // 失败/待确认样例，限流留存
}

const maxScrapeFailureSamples = 20

var errScrapeBusy = errors.New("刮削任务正在进行中")
var errScrapeIndexRefresh = errors.New("刮削文件已保存，但索引刷新未确认；请检查存储后重新扫库")

// claimNFO 独占 NFO 写入通道。kind 为 scan / probe / scrape / scrape_avatars / translate / edit。
// 独立翻译还持有自己的单任务门，整个翻译与逐片索引刷新期间占用 translate 通道。
//
// 同类型探测等任务通过引用计数共用通道；独立刮削与人工编辑/重读源必须独占，
// 避免 NFO 写入、归属记录与索引刷新使用不同的旧快照。
//
// 注意 scrape 与 scrape_avatars 用的是**不同 kind**，因此二者互斥而不是可并行——
// 需求原计划写的是「可并行（写不同表）」，但头像任务要往每部参演影片的 NFO 里写
// <actor><thumb>（真源），与影片刮削的「读全文 → 改局部 → 原子写」是同一个文件，
// 并发会互相覆盖。这里按「NFO 写入必须串行」这条更根本的约束处理。
func (a *App) claimNFO(kind string) bool {
	a.nfoGate.Lock()
	defer a.nfoGate.Unlock()
	if a.nfoOwner != "" && (a.nfoOwner != kind || kind == "scrape" || kind == "edit" || kind == "artwork" || kind == "sprites" || kind == "subtitle_marks") {
		return false
	}
	a.nfoOwner = kind
	a.nfoRefs++
	return true
}

func (a *App) releaseNFO(kind string) {
	a.nfoGate.Lock()
	defer a.nfoGate.Unlock()
	if a.nfoOwner != kind {
		return
	}
	// 同一持有者允许多层占用（批量探测与单条探测都算 "probe"）：
	// 逐层递减，最后一层退出才真正让出通道；否则先结束的那个会把 owner
	// 清空，另一个仍在写 NFO 的任务就失去保护了。
	if a.nfoRefs > 0 {
		a.nfoRefs--
	}
	if a.nfoRefs == 0 {
		a.nfoOwner = ""
	}
}

// nfoBusyOwner 返回当前占用者的中文描述，供冲突提示。
func (a *App) nfoBusyOwner() string {
	a.nfoGate.Lock()
	defer a.nfoGate.Unlock()
	switch a.nfoOwner {
	case "scan":
		return "扫库"
	case "probe":
		return "媒体信息探测"
	case "scrape":
		return "刮削"
	case "scrape_avatars":
		return "演员头像任务"
	case "translate":
		return "翻译"
	case "edit":
		return "元数据编辑或重读源"
	case "artwork":
		return "图片操作"
	case "sprites":
		return "视频预览生成"
	case "subtitle_marks":
		return "字幕标记整理"
	}
	return ""
}

// —— 状态读写 ——

// beginScrape 申请启动一类刮削任务：并发门 + 状态置位。
// 关停窗口内返回 errServerShuttingDown：rootCtx 门在已持有的 scrapeTaskMu 内判断，
// 保证「不会被 workerWG.Wait 覆盖的登记」发生在 Wait 之前。
func (a *App) beginScrape(kind string, total int) error {
	a.scrapeTaskMu.Lock()
	defer a.scrapeTaskMu.Unlock()
	if a.rootCtx.Err() != nil {
		return errServerShuttingDown
	}
	if a.scrapeStatus.Running {
		return errScrapeBusy
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	a.scrapeCtx, a.scrapeCancel = ctx, cancel
	a.scrapeStatus = scrapeStatus{Running: true, Kind: kind, Total: total, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	return nil
}

func (a *App) updateScrapeProgress(progress scraper.Progress) {
	a.scrapeTaskMu.Lock()
	defer a.scrapeTaskMu.Unlock()
	status := &a.scrapeStatus
	status.Total, status.Done = progress.Total, progress.Done
	status.Success, status.Skipped, status.Failed = progress.Success, progress.Skipped, progress.Failed
	status.Current = progress.Current
}

func (a *App) addScrapeFailure(sample string) {
	a.scrapeTaskMu.Lock()
	defer a.scrapeTaskMu.Unlock()
	if len(a.scrapeStatus.Failures) >= maxScrapeFailureSamples {
		return
	}
	a.scrapeStatus.Failures = append(a.scrapeStatus.Failures, sample)
}

func (a *App) endScrape(err error) {
	a.scrapeTaskMu.Lock()
	status := &a.scrapeStatus
	status.Running = false
	status.Current = ""
	status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	ctx := a.scrapeCtx
	// 人工取消时 scraper 返回的是 ctx.Err()（context.Canceled），err 恒非 nil，
	// 所以不能只认 err==nil：那样取消会被记成失败、Cancelled 永远为 false。
	status.Cancelled = ctx != nil && ctx.Err() != nil &&
		(err == nil || errors.Is(err, context.Canceled))
	if status.IndexRefreshFailed {
		// Cancellation does not make saved-but-unindexed output disappear.
		status.Error = errScrapeIndexRefresh.Error()
	} else if errors.Is(err, scraper.ErrAvatarWriteIncomplete) {
		// Cancellation must not hide saved avatar output requiring repair.
		status.Error = scraper.ErrAvatarWriteIncomplete.Error()
	} else if err != nil && !status.Cancelled {
		status.Error = err.Error()
	}
	// 必须调用 cancel 回收 context：否则每次任务结束都会在 rootCtx 上
	// 留一个永不释放的 child；同时清空 scrapeCtx，让后续 scrapeContext()
	// 回退到 rootCtx，而不是继续返回已结束任务的陈旧 ctx。
	if a.scrapeCancel != nil {
		a.scrapeCancel()
	}
	a.scrapeCancel = nil
	a.scrapeCtx = nil
	a.scrapeTaskMu.Unlock()
}

func (a *App) scrapeContext() context.Context {
	a.scrapeTaskMu.RLock()
	defer a.scrapeTaskMu.RUnlock()
	if a.scrapeCtx != nil {
		return a.scrapeCtx
	}
	return a.rootCtx
}

func (a *App) adminScrapeProgress(c *gin.Context) {
	a.scrapeTaskMu.RLock()
	status := a.scrapeStatus
	// Failures 是切片：浅拷贝后样本 append 会写进正在被 worker 追加的同底层数组，
	// 与 JSON 编码并发即数据竞争（且可能看到半写状态）。整份深拷贝后再编码。
	status.Failures = append([]string(nil), status.Failures...)
	a.scrapeTaskMu.RUnlock()
	c.JSON(http.StatusOK, status)
}

func (a *App) adminScrapeCancel(c *gin.Context) {
	if !a.admitAdminOperation(c) {
		return
	}

	a.scrapeTaskMu.Lock()
	cancel, running := a.scrapeCancel, a.scrapeStatus.Running
	a.scrapeTaskMu.Unlock()
	if !running || cancel == nil {
		apiError(c, http.StatusConflict, "当前没有刮削任务")
		return
	}
	cancel()
	c.JSON(http.StatusOK, gin.H{"status": "cancelling"})
}

// —— 启动任务 ——

type scrapeRunRequest struct {
	LibraryID   int64 `json:"library_id"`
	OnlyMissing *bool `json:"only_missing"`
	Limit       int   `json:"limit"`
	Overwrite   *bool `json:"overwrite"`
}

// adminScrapeRun 启动批量刮削（异步，进度走 /scrape/progress）。
func (a *App) adminScrapeRun(c *gin.Context) {
	var req scrapeRunRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			apiError(c, http.StatusBadRequest, "invalid json")
			return
		}
	}
	if value := c.Query("library_id"); value != "" && req.LibraryID == 0 {
		req.LibraryID, _ = strconv.ParseInt(value, 10, 64)
	}
	onlyMissing := true
	if req.OnlyMissing != nil {
		onlyMissing = *req.OnlyMissing
	}
	if !a.admitAdminOperation(c) {
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	// 关停门：登记门已关闭意味着正在 drain（srv.Shutdown 与 WaitBackground 并发跑），
	// 此时再进真实逻辑会读库、占 NFO 通道并让 worker 与关库并发。
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	started := false
	defer func() {
		if !started {
			release() // 提前返回（校验失败/空集）时归还登记，避免 workerWG 计数泄漏
		}
	}()
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		apiError(c, http.StatusBadRequest, "尚未配置 MetaTube 地址或 token")
		return
	}
	if req.Overwrite != nil {
		cfg.Overwrite = *req.Overwrite
	}
	if owner := a.nfoBusyOwner(); owner != "" {
		apiError(c, http.StatusConflict, owner+"正在进行中，请稍后再试")
		return
	}
	total, err := a.db.CountMoviesForScrape(req.LibraryID, onlyMissing)
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if total == 0 {
		c.JSON(http.StatusOK, gin.H{"total": 0, "message": "没有符合条件的影片"})
		return
	}
	if err := a.startScrapeTask("scrape", total); err != nil {
		workerGateAPIError(c, err, http.StatusConflict)
		return
	}
	started = true
	go func() {
		defer release()
		_ = a.executeScrape(cfg, scraper.RunOptions{
			LibraryID: req.LibraryID, OnlyMissing: onlyMissing, Limit: req.Limit, Overwrite: cfg.Overwrite,
		})
	}()
	slog.Info("刮削任务已启动", "total", total, "library_id", req.LibraryID, "only_missing", onlyMissing, "overwrite", cfg.Overwrite)
	c.JSON(http.StatusAccepted, gin.H{"total": total})
}

// executeScrape 同步跑完一批刮削并完成全部收尾（调用方负责起 goroutine）。
// 同步形态是为了让计划任务能据此判定重叠与成败。
func (a *App) executeScrape(cfg scraper.Config, opts scraper.RunOptions) error {
	started := time.Now()
	taskID := a.startTask("scrape")
	opts.OnProgress = a.updateScrapeProgress
	opts.OnItem = func(item scraper.ItemResult) {
		// 覆盖同名图片后必须显式失效 tag 缓存，否则客户端几分钟内仍拿旧图。
		if len(item.Images) > 0 {
			a.invalidateImageTag(item.Images...)
		}
		if item.Status != scraper.StatusSuccess || item.Reason != "" {
			a.addScrapeFailure(displayItem(item))
		}
	}
	// NFO 通道只在这段刮削期间持有：收尾的整库扫描会自己重新申请，
	// 若仍持锁会被 claimNFO 判为「他人占用」而拒绝（互斥是按占用者类型判定的）。
	// claim/release 必须成对出现在同一函数体内：占用放在 goroutine 内部，
	// 而不是放在 handler 的 startScrapeTask 里——那样一旦 goroutine 起不来
	// （panic），nfoOwner 会永久停留在 "scrape"，扫库/探测全部被拒。
	progress, err := func() (scraper.Progress, error) {
		if !a.claimNFO("scrape") {
			return scraper.Progress{}, errScrapeBusy
		}
		defer a.releaseNFO("scrape")
		return scraper.New(cfg, a.db).Run(a.scrapeContext(), opts)
	}()
	// 取消或熔断不撤销此前已落盘的成功项，仍须导入这些 NFO。
	// 扫描不使用已取消的刮削 context。
	if progress.Success > 0 {
		if _, scanErr := a.scanLibraries(0, 0, scanner.ScanModeFull, false); scanErr != nil {
			a.scrapeTaskMu.Lock()
			a.scrapeStatus.IndexRefreshFailed = true
			a.scrapeTaskMu.Unlock()
			err = errors.Join(err, errScrapeIndexRefresh)
			slog.Warn("刮削后整库扫描失败，索引刷新未确认")
		}
	}
	a.cache.Clear()
	a.finishTask(taskID, err)
	a.endScrape(err)
	// 结束日志必须自解释：作用域 + 成败计数 + 耗时 + 失败首因。
	// 用户反馈「只有已开始/已结束」，根因就是这条只有计数、没有范围与样例首因。
	fields := []any{"task_id", taskID, "scope", scrapeScope(opts),
		"success", progress.Success, "skipped", progress.Skipped,
		"failed", progress.Failed, "total", progress.Total,
		"elapsed", time.Since(started).Round(time.Millisecond).String()}
	if first := scheduledFirstSample(a.scrapeFailureSamples()); first != "" {
		fields = append(fields, "first_failure", first)
	}
	if err != nil {
		fields = append(fields, "error", err)
	}
	slog.Info("刮削任务结束", fields...)
	return err
}

// scrapeScope 渲染一批刮削的有效作用域。计划任务与手动任务共用 execute* 路径，
// 结束日志自己说清范围，才不用回头找「启动」那条日志去配对。
func scrapeScope(opts scraper.RunOptions) string {
	return fmt.Sprintf("library_id=%d limit=%d only_missing=%t overwrite=%t",
		opts.LibraryID, opts.Limit, opts.OnlyMissing, opts.Overwrite)
}

// scrapeFailureSamples 返回限量失败样例的副本（日志汇总用；与 /scrape/progress 同源）。
func (a *App) scrapeFailureSamples() []string {
	a.scrapeTaskMu.RLock()
	defer a.scrapeTaskMu.RUnlock()
	return append([]string(nil), a.scrapeStatus.Failures...)
}

// adminScrapeAvatars 启动演员头像任务（异步）。
func (a *App) adminScrapeAvatars(c *gin.Context) {
	var req struct {
		LibraryID int64 `json:"library_id"`
		Limit     int   `json:"limit"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			apiError(c, http.StatusBadRequest, "invalid json")
			return
		}
	}
	if value := c.Query("library_id"); value != "" && req.LibraryID == 0 {
		req.LibraryID, _ = strconv.ParseInt(value, 10, 64)
	}
	if !a.admitAdminOperation(c) {
		return
	}
	// 关停门：同 adminScrapeRun。
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		apiError(c, http.StatusBadRequest, "尚未配置 MetaTube 地址或 token")
		return
	}
	if owner := a.nfoBusyOwner(); owner != "" {
		apiError(c, http.StatusConflict, owner+"正在进行中，请稍后再试")
		return
	}
	names, err := a.db.ActorsMissingAvatar(req.LibraryID, req.Limit)
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if len(names) == 0 {
		c.JSON(http.StatusOK, gin.H{"total": 0, "message": "没有缺头像的演员（头像任务只补缺失，是幂等的）"})
		return
	}
	if err := a.startScrapeTask("scrape_avatars", len(names)); err != nil {
		workerGateAPIError(c, err, http.StatusConflict)
		return
	}
	started = true
	go func() {
		defer release()
		_ = a.executeScrapeAvatars(cfg, req.LibraryID, req.Limit)
	}()
	slog.Info("演员头像任务已启动", "actors", len(names), "library_id", req.LibraryID)
	c.JSON(http.StatusAccepted, gin.H{"total": len(names)})
}

// executeScrapeAvatars 同步跑完头像任务并收尾（与影片刮削同样的同步形态）。
func (a *App) executeScrapeAvatars(cfg scraper.Config, libraryID int64, limit int) error {
	started := time.Now()
	scope := fmt.Sprintf("library_id=%d limit=%d", libraryID, limit)
	taskID := a.startTask("scrape_avatars")
	// claim/release 成对放在本函数内（与 executeScrape 同一理由）：goroutine
	// 起不来时不会把 nfoOwner 永久留在 "scrape_avatars"。
	progress, err := func() (scraper.Progress, error) {
		if !a.claimNFO("scrape_avatars") {
			return scraper.Progress{}, errScrapeBusy
		}
		defer a.releaseNFO("scrape_avatars")
		return scraper.New(cfg, a.db).RunAvatars(a.scrapeContext(), libraryID, limit,
			a.updateScrapeProgress, func(result scraper.AvatarResult) {
				if result.Status != scraper.StatusSuccess {
					a.addScrapeFailure("演员 " + result.Actor + "：" + result.Reason)
				}
			})
	}()
	elapsed := time.Since(started).Round(time.Millisecond).String()
	fields := []any{"task_id", taskID, "scope", scope, "success", progress.Success,
		"skipped", progress.Skipped, "failed", progress.Failed, "total", progress.Total,
		"elapsed", elapsed}
	if first := scheduledFirstSample(a.scrapeFailureSamples()); first != "" {
		fields = append(fields, "first_failure", first)
	}
	if err != nil {
		a.finishTask(taskID, err)
		a.endScrape(err)
		a.cache.Clear()
		// 提前结束也要带计数与首因：只写 error 的日志看不出已经处理了多少、
		// 失败的是哪一条，等于把「为什么失败」又丢回给了任务行。
		fields = append(fields, "error", err)
		slog.Warn("演员头像任务提前结束", fields...)
		return err
	}
	a.finishTask(taskID, err)
	a.endScrape(err)
	a.cache.Clear()
	slog.Info("演员头像任务结束", fields...)
	return err
}

// displayItem 把单条刮削结果格式化成可读的一行（进失败样例列表）。
func displayItem(item scraper.ItemResult) string {
	prefix := "失败"
	if item.Status == scraper.StatusSkipped {
		prefix = "跳过"
	} else if item.Status == scraper.StatusSuccess {
		prefix = "已保存待核对"
	}
	// 待人工确认的原因已经带了前缀，避免叠加成「跳过 [待人工确认]」。
	reason := strings.TrimPrefix(item.Reason, store.ScrapeConfirmPrefix)
	return prefix + " " + item.Title + "：" + reason
}

// —— 单条手动刮削：预览 → 确认 ——

func (a *App) adminScrapePreview(c *gin.Context) {
	// Presence matters: ?q= is an invalid manual request, not automatic search.
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		apiError(c, http.StatusBadRequest, "invalid query parameters")
		return
	}
	var manual scraper.ManualSearch
	for name, target := range map[string]**string{"q": &manual.Query, "metatube_id": &manual.MetaTubeID} {
		if entries, exists := values[name]; exists {
			if len(entries) != 1 {
				apiError(c, http.StatusBadRequest, name+" must occur exactly once")
				return
			}
			*target = &entries[0]
		}
	}
	explicit := manual.Query != nil || manual.MetaTubeID != nil
	if explicit {
		if err := manual.Validate(); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	movie, ok := a.movieParam(c)
	if !ok {
		return
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		apiError(c, http.StatusBadRequest, "尚未配置 MetaTube 地址或 token")
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	s := scraper.New(cfg, a.db)
	var preview scraper.PreviewResult
	if explicit {
		preview, err = s.PreviewManual(ctx, movie, manual)
	} else {
		preview, err = s.Preview(ctx, movie)
	}
	if err != nil {
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, preview)
}

// adminScrapeInspect 取某个候选的详情并与现有 NFO 做字段 diff（只显示原文，不翻译、不下图）。
func (a *App) adminScrapeInspect(c *gin.Context) {
	movie, ok := a.movieParam(c)
	if !ok {
		return
	}
	var req struct {
		Provider string   `json:"provider"`
		ID       string   `json:"id"`
		Update   *bool    `json:"update"`
		Position *float64 `json:"position"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Provider == "" || req.ID == "" {
		apiError(c, http.StatusBadRequest, "provider 与 id 必填")
		return
	}
	if !validPosition(req.Position) {
		apiError(c, http.StatusBadRequest, "position 必须是 [0, 1] 内的数字")
		return
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		apiError(c, http.StatusBadRequest, "尚未配置 MetaTube 地址或 token")
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	client := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
	pid := metatube.ProviderID{Provider: req.Provider, ID: req.ID, Position: req.Position, Update: req.Update}
	info, err := client.MovieInfo(ctx, pid.Provider, pid.ID, pid.Lazy())
	if err != nil {
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, a.buildInspect(cfg, movie, info, pid.Position))
}

// adminScrapeConfirm 确认写入（无状态预览：按 provider:id 重新取详情后落盘）。
func (a *App) adminScrapeConfirm(c *gin.Context) {
	movie, ok := a.movieParam(c)
	if !ok {
		return
	}
	var req struct {
		Provider  string   `json:"provider"`
		ID        string   `json:"id"`
		Overwrite *bool    `json:"overwrite"`
		Update    *bool    `json:"update"`
		Position  *float64 `json:"position"`
		Stills    *struct {
			Version string `json:"version"`
			Indices []*int `json:"indices"`
		} `json:"stills"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Provider == "" || req.ID == "" {
		apiError(c, http.StatusBadRequest, "provider 与 id 必填")
		return
	}
	if !validPosition(req.Position) {
		apiError(c, http.StatusBadRequest, "position 必须是 [0, 1] 内的数字")
		return
	}
	if req.Stills != nil {
		selection := req.Stills
		_, err := hex.DecodeString(selection.Version)
		if len(selection.Version) != 64 || err != nil || selection.Version != strings.ToLower(selection.Version) || len(selection.Indices) == 0 || len(selection.Indices) > maxScrapeStills {
			apiError(c, http.StatusBadRequest, "stills 必须包含有效列表版本和 1 至 24 个下标")
			return
		}
		seen := make(map[int]bool, len(selection.Indices))
		for _, index := range selection.Indices {
			if index == nil || *index < 0 || *index >= maxScrapeStills || seen[*index] {
				apiError(c, http.StatusBadRequest, "剧照下标必须唯一且在 [0, 23] 内")
				return
			}
			seen[*index] = true
		}
	}
	if !a.admitAdminOperation(c) {
		return
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		apiError(c, http.StatusBadRequest, "尚未配置 MetaTube 地址或 token")
		return
	}
	if !a.claimNFO("scrape") {
		apiError(c, http.StatusConflict, a.nfoBusyOwner()+"正在进行中，请稍后再试")
		return
	}
	defer a.releaseNFO("scrape")
	// movieParam 在取得写入通道前读取；归属必须使用上一次持有者提交后的记录。
	movie, err := a.db.Movie(movie.ID)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	overwrite := cfg.Overwrite
	if req.Overwrite != nil {
		overwrite = *req.Overwrite
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	pid := metatube.ProviderID{Provider: req.Provider, ID: req.ID, Position: req.Position, Update: req.Update}
	s := scraper.New(cfg, a.db)
	var result scraper.ApplyResult
	if req.Stills == nil {
		result, err = s.ScrapeOneMovie(ctx, movie, pid, overwrite)
	} else {
		// 确认必须重新取详情核对列表，不能仅信任仍存活的预览缓存。
		client := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
		info, lookupErr := client.MovieInfo(ctx, pid.Provider, pid.ID, pid.Lazy())
		if lookupErr != nil {
			apiError(c, http.StatusBadGateway, lookupErr.Error())
			return
		}
		urls := stillURLs(info)
		_, version := encodeStillList(urls)
		if version != req.Stills.Version {
			apiError(c, http.StatusConflict, "剧照列表已变化，请重新预览后确认")
			return
		}
		selected := make([]string, 0, len(req.Stills.Indices))
		for _, index := range req.Stills.Indices {
			if *index >= len(urls) {
				apiError(c, http.StatusBadRequest, "剧照下标超出列表范围")
				return
			}
			selected = append(selected, urls[*index])
		}
		result, err = s.Apply(ctx, movie, info, scraper.ApplyOptions{
			Provider: pid.Provider, ID: pid.ID, Position: pid.Position,
			Overwrite: overwrite, Refresh: true, StillURLs: selected,
		})
	}
	// 覆盖同名图片后立即失效 tag 缓存。
	if len(result.Images) > 0 {
		a.invalidateImageTag(result.Images...)
	}
	if movie.NFOPath != "" {
		a.invalidateNFOStreams(movie.NFOPath)
	}
	a.cache.Clear()
	if err != nil {
		_ = a.db.SetScrapeResult(movie.ID, err.Error())
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	slog.Info("单条刮削完成", "movie_id", movie.ID, "provider", req.Provider, "id", req.ID,
		"images", result.ImageCount, "overwrite", overwrite)
	response := gin.H{
		"status": "success", "provider": result.Provider, "number": result.Number,
		"title": result.Title, "images": result.ImageCount, "warnings": result.ImageWarn,
	}
	if result.AutoMarksError != "" {
		response["auto_marks_error"] = "NFO 已保存，自动标记归属记录失败；请人工核对标签与归属记录，不要直接重新刮削；重扫不能恢复归属记录。"
	}
	if result.RefreshError != "" {
		// NFO 已提交；提示通过重扫恢复，不能把它当作未写入或暴露底层路径/SQL。
		response["refresh_error"] = "NFO 已保存，索引刷新失败；请重新扫描该影片或媒体库。"
	}
	c.JSON(http.StatusOK, response)
}

// adminScrapeImage 图片代理：预览用的缩略图**只转发不落盘**。
//
// 必须由本服务代理：MetaTube 常部署在内网或 http 上，直接给浏览器地址会被
// 混合内容拦截或根本不可达。
func (a *App) adminScrapeImage(c *gin.Context) {
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		c.Status(http.StatusBadRequest)
		return
	}
	kind := strings.ToLower(c.Query("kind"))
	switch kind {
	case "primary", "thumb", "backdrop", "still":
	default:
		apiError(c, http.StatusBadRequest, "unsupported image kind")
		return
	}
	provider, id := c.Query("provider"), c.Query("id")
	if provider == "" || id == "" {
		apiError(c, http.StatusBadRequest, "provider 与 id 必填")
		return
	}
	// ratio/pos 只在 primary 生效：上游 thumb/backdrop 用固定常量裁切，
	// 别处传进来只会被忽略，静默忽略比直接拒绝更容易让人误以为生效。
	ratio, err := optionalFloatQuery(c, "ratio")
	if err != nil || !validRatio(ratio) {
		apiError(c, http.StatusBadRequest, "ratio 必须是 [0.01, 100] 内的数字")
		return
	}
	position, err := optionalFloatQuery(c, "pos")
	if err != nil || !validPosition(position) {
		apiError(c, http.StatusBadRequest, "pos 必须是 [0, 1] 内的数字")
		return
	}
	// 剧照的裁切由服务端固定（16:9 居中），调用方传裁切参数一律拒绝：
	// 静默忽略会让人误以为自己的比例生效了。
	if kind == "still" {
		if ratio != nil || position != nil {
			apiError(c, http.StatusBadRequest, "剧照不支持 ratio 与 pos")
			return
		}
	} else if kind != "primary" && (ratio != nil || position != nil) {
		apiError(c, http.StatusBadRequest, "ratio 与 pos 只支持 primary")
		return
	}
	// 新预览用 version + index 绑定列表快照；不带 version 的旧链接保留原语义。
	stillVersion := c.Query("version")
	if _, err := hex.DecodeString(stillVersion); stillVersion != "" && (kind != "still" || len(stillVersion) != 64 || err != nil || stillVersion != strings.ToLower(stillVersion)) {
		apiError(c, http.StatusBadRequest, "version 必须是剧照列表的 64 位小写十六进制版本")
		return
	}
	// index 是剧照的受限候选标识：客户端只提交下标，地址由服务端重新解析。
	stillIndex := -1
	if kind == "still" {
		value, err := strconv.Atoi(strings.TrimSpace(c.Query("index")))
		if err != nil || value < 0 {
			apiError(c, http.StatusBadRequest, "index 必须是非负整数")
			return
		}
		stillIndex = value
	}
	// 同一张图在一次预览里会被反复要（切候选、重开抽屉），结果只依赖
	// provider/id/kind 与裁切参数，进程内短缓存即可，避免重复打上游。
	// 裁切参数必须进 key，否则换位置后会拿到上一张裁好的图。
	cacheKey := joinCacheKey("scrapeimg", kind, provider, id, cropKey(ratio, position))
	if kind == "still" {
		cacheKey += ":i=" + strconv.Itoa(stillIndex)
		if stillVersion != "" {
			cacheKey += ":v=" + stillVersion
		}
	}
	// 剧照 URL 带列表版本，不同列表不会命中同一个浏览器缓存；版本是地址列表
	// 的摘要，不承诺同一远端 URL 的图片字节永久不变。失败响应不加缓存头。
	serveThumb := func(raw []byte) {
		c.Header("Cache-Control", "public, max-age=300, must-revalidate")
		c.Data(http.StatusOK, "image/jpeg", raw)
	}
	if raw, ok := a.scrapeThumbs.Get(cacheKey); ok {
		serveThumb(raw)
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	// 下载名额满时立即拒绝，不让预览图片请求排队；字节缓存命中无需名额。
	select {
	case a.scrapeSem <- struct{}{}:
		defer func() { <-a.scrapeSem }()
	default:
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if raw, ok := a.scrapeThumbs.Get(cacheKey); ok { // 取得名额前可能已被别的请求填上
		serveThumb(raw)
		return
	}
	client := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
	// 预览缩略图不带 badge：候选列表里贴角标只会干扰挑选。
	options := metatube.ImageOptions{Quality: 70, Ratio: ratio, Position: position}
	upstreamKind := kind
	if kind == "still" {
		urls, err := a.scrapeStillURLs(ctx, client, provider, id, stillVersion)
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		if stillIndex >= len(urls) {
			c.Status(http.StatusNotFound)
			return
		}
		// 剧照在上游走 primary + url= 分支：样张多是横图，固定 16:9 居中。
		upstreamKind = "primary"
		options = metatube.ImageOptions{
			Quality: 70, RemoteURL: urls[stillIndex],
			Ratio: &stillRatio, Position: &stillPosition,
		}
	}
	data, err := client.Download(ctx, client.ImageURL(upstreamKind, provider, id, options), maxScrapeImageBytes)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	if len(data) <= scrapeImageCacheMax {
		a.scrapeThumbs.Set(cacheKey, data, scrapeImageTTL)
	}
	// 上游给的是 JPEG；原样转发，浏览器直接能显示，无需转码。
	serveThumb(data)
}

// scrapeStillURLs 优先使用 inspect 缓存的版本快照。快照过期或被淘汰后，
// 只在上游列表仍为同一版本时重建；列表变化就返回空列表，由代理回 404，不能偷换下标。
// 无版本的旧链接仍按 (provider,id) 缓存当前列表，不保证跨次 inspect 的稳定性。
func (a *App) scrapeStillURLs(ctx context.Context, client *metatube.Client, provider, id, version string) ([]string, error) {
	key := joinCacheKey("scrapestills", provider, id)
	if version != "" {
		key = joinCacheKey("scrapestills", provider, id, version)
	}
	if raw, ok := a.scrapeStills.Get(key); ok {
		var urls []string
		err := json.Unmarshal(raw, &urls)
		return urls, err
	}
	info, err := client.MovieInfo(ctx, provider, id, true)
	if err != nil {
		return nil, err
	}
	urls := stillURLs(info)
	raw, currentVersion := encodeStillList(urls)
	if version != "" && version != currentVersion {
		return nil, nil
	}
	a.scrapeStills.Set(key, raw, scrapeImageTTL)
	return urls, nil
}

// optionalFloatQuery 读取可选浮点查询参数：缺省返回 nil，非法返回错误。
// 查询串是不可信输入，只在这里解析一次，之后按具体值使用。
func optionalFloatQuery(c *gin.Context, name string) (*float64, error) {
	values, present := c.Request.URL.Query()[name]
	if !present {
		return nil, nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(values[0]), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("非法数字")
	}
	return &value, nil
}

// validRatio 对齐上游裁切区间：超出 [0.01, 100] 时上游不裁切，这里直接拒绝。
func validRatio(ratio *float64) bool {
	return ratio == nil || (*ratio >= 0.01 && *ratio <= 100)
}

// validPosition 校验裁切位置：上游按图片长边比例解释，取值 [0, 1]。
func validPosition(position *float64) bool {
	return position == nil || (*position >= 0 && *position <= 1)
}

// cropKey 把裁切参数拼进缓存 key；缺省不带后缀，旧 key 保持原样。
// joinCacheKey 把多段值编成互不歧义的缓存 key：每段都带字节长度前缀。
//
// provider/id 来自查询参数，属于不可信输入，任何字符都可能出现；直接拼 ':' 会让
// (provider="a:b", id="c") 与 (provider="a", id="b:c") 撞成一条，互相冒充缓存条目。
func joinCacheKey(prefix string, parts ...string) string {
	key := prefix
	for _, part := range parts {
		key += ":" + strconv.Itoa(len(part)) + ":" + part
	}
	return key
}

func cropKey(ratio, position *float64) string {
	key := ""
	if ratio != nil {
		key += ":r=" + strconv.FormatFloat(*ratio, 'g', -1, 64)
	}
	if position != nil {
		key += ":p=" + strconv.FormatFloat(*position, 'g', -1, 64)
	}
	return key
}

const (
	// maxScrapeImageBytes 单张预览图的下载上限，避免上游返回异常大文件把内存打满。
	maxScrapeImageBytes = 8 << 20
	// scrapeImageCacheMax 超过这个体积就不进缓存（缓存条目按条数计上限）。
	scrapeImageCacheMax = 256 << 10
	// scrapeImageTTL 预览缩略图的进程内缓存时长。
	scrapeImageTTL = 10 * time.Minute
	// scrapeThumbCacheSize 预览图字节缓存的条目上限。
	scrapeThumbCacheSize = 512
	// maxScrapeImageConcurrency 同时向上游拉取的预览图数量上限。
	maxScrapeImageConcurrency = 4
	// scrapeStillCacheSize 剧照地址列表的进程内条目上限（各版本快照分别计数）。
	scrapeStillCacheSize = 256
)

// 剧照预览固定 16:9 居中裁切：样张多是横图，主海报的 2/3 会把画面裁得只剩中间一条。
var (
	stillRatio    = 16.0 / 9.0
	stillPosition = 0.5
)

// movieParam 解析并校验路径里的影片 id。
func (a *App) movieParam(c *gin.Context) (store.Movie, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return store.Movie{}, false
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return store.Movie{}, false
	}
	return movie, true
}

// adminScrapeCandidates 返回本次批量刮削会处理多少条，供界面在点「开始」前预览。
func (a *App) adminScrapeCandidates(c *gin.Context) {
	libraryID, _ := strconv.ParseInt(c.Query("library_id"), 10, 64)
	onlyMissing := c.DefaultQuery("only_missing", "true") != "false"
	total, err := a.db.CountMoviesForScrape(libraryID, onlyMissing)
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "library_id": libraryID, "only_missing": onlyMissing})
}

// adminScrapeTest 测试连接（需求 5h）：MetaTube 打 GET /，Deepl-Proxy 打一次最小翻译。
func (a *App) adminScrapeTest(c *gin.Context) {
	var req struct {
		Target string `json:"target"`
	}
	_ = c.ShouldBindJSON(&req)
	cfg := a.scrapeConfig()
	ctx, cancel := a.requestContext(c)
	defer cancel()
	start := time.Now()
	switch strings.ToLower(strings.TrimSpace(req.Target)) {
	case "translate":
		if !cfg.Translate.Enabled() {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "未配置翻译 api_url / api_key"})
			return
		}
		text, err := a.scraperFor(cfg).Translator().Ping(ctx)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "elapsed_ms": elapsedMS(start)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "detail": "翻译返回：" + text, "elapsed_ms": elapsedMS(start)})
	default:
		if strings.TrimSpace(cfg.BaseURL) == "" {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "未配置 MetaTube 地址"})
			return
		}
		client := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
		if err := client.Health(ctx); err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "elapsed_ms": elapsedMS(start)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "detail": "MetaTube 可达", "elapsed_ms": elapsedMS(start)})
	}
}

func elapsedMS(start time.Time) int64 { return time.Since(start).Milliseconds() }

// scraperFor 按配置构造刮削器（每次读最新配置，保存即时生效）。
func (a *App) scraperFor(cfg scraper.Config) *scraper.Scraper {
	return scraper.New(cfg, a.db)
}

// startScrapeTask 申请启动一类刮削任务（互斥 + 状态置位）。
// 返回错误时调用方负责写 HTTP 响应：errScrapeBusy → 409，errServerShuttingDown → 503。
//
// 这里只做并发门（beginScrape），不再 claimNFO：NFO 通道的占用改由
// executeScrape / executeScrapeAvatars 在自己的 goroutine 里 claim+defer release，
// 保证「占用与释放」在同一个栈上，goroutine 起不来也不会把通道永久锁死。
func (a *App) startScrapeTask(kind string, total int) error {
	return a.beginScrape(kind, total)
}
