package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/metatube"
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
	Running    bool     `json:"running"`
	Kind       string   `json:"kind"` // scrape / scrape_avatars
	Total      int      `json:"total"`
	Done       int      `json:"done"`
	Success    int      `json:"success"`
	Skipped    int      `json:"skipped"`
	Failed     int      `json:"failed"`
	Current    string   `json:"current"`
	StartedAt  string   `json:"started_at,omitempty"`
	FinishedAt string   `json:"finished_at,omitempty"`
	Error      string   `json:"error,omitempty"`
	Cancelled  bool     `json:"cancelled"`
	Failures   []string `json:"failures,omitempty"` // 失败/待确认样例，限流留存
}

const maxScrapeFailureSamples = 20

var errScrapeBusy = errors.New("刮削任务正在进行中")

// claimNFO 独占 NFO 写入通道。kind 为 scan / probe / scrape / scrape_avatars。
//
// 已持有同一 kind 时视为可重入（例如探测内部再调扫描），返回 true 不重复占用。
//
// 注意 scrape 与 scrape_avatars 用的是**不同 kind**，因此二者互斥而不是可并行——
// 需求原计划写的是「可并行（写不同表）」，但头像任务要往每部参演影片的 NFO 里写
// <actor><thumb>（真源），与影片刮削的「读全文 → 改局部 → 原子写」是同一个文件，
// 并发会互相覆盖。这里按「NFO 写入必须串行」这条更根本的约束处理。
func (a *App) claimNFO(kind string) bool {
	a.nfoGate.Lock()
	defer a.nfoGate.Unlock()
	if a.nfoOwner != "" && a.nfoOwner != kind {
		return false
	}
	a.nfoOwner = kind
	return true
}

func (a *App) releaseNFO(kind string) {
	a.nfoGate.Lock()
	defer a.nfoGate.Unlock()
	if a.nfoOwner == kind {
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
	}
	return ""
}

// —— 状态读写 ——

func (a *App) beginScrape(kind string, total int) bool {
	a.scrapeTaskMu.Lock()
	defer a.scrapeTaskMu.Unlock()
	if a.scrapeStatus.Running {
		return false
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	a.scrapeCtx, a.scrapeCancel = ctx, cancel
	a.scrapeStatus = scrapeStatus{Running: true, Kind: kind, Total: total, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	return true
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
	status.Cancelled = ctx != nil && ctx.Err() != nil && err == nil
	if err != nil {
		status.Error = err.Error()
	}
	a.scrapeCancel = nil
	a.scrapeTaskMu.Unlock()
}

func (a *App) scraping() bool {
	a.scrapeTaskMu.RLock()
	defer a.scrapeTaskMu.RUnlock()
	return a.scrapeStatus.Running
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
	a.scrapeTaskMu.RUnlock()
	c.JSON(http.StatusOK, status)
}

func (a *App) adminScrapeCancel(c *gin.Context) {
	a.scrapeTaskMu.Lock()
	cancel, running := a.scrapeCancel, a.scrapeStatus.Running
	a.scrapeTaskMu.Unlock()
	if !running || cancel == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "当前没有刮削任务"})
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
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
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
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置 MetaTube 地址或 token"})
		return
	}
	if req.Overwrite != nil {
		cfg.Overwrite = *req.Overwrite
	}
	if owner := a.nfoBusyOwner(); owner != "" {
		c.JSON(http.StatusConflict, gin.H{"error": owner + "正在进行中，请稍后再试"})
		return
	}
	total, err := a.db.CountMoviesForScrape(req.LibraryID, onlyMissing)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if total == 0 {
		c.JSON(http.StatusOK, gin.H{"total": 0, "message": "没有符合条件的影片"})
		return
	}
	if !a.startScrapeTask(c, "scrape", total) {
		return
	}
	go a.executeScrape(cfg, scraper.RunOptions{
		LibraryID: req.LibraryID, OnlyMissing: onlyMissing, Limit: req.Limit, Overwrite: cfg.Overwrite,
	})
	slog.Info("刮削任务已启动", "total", total, "library_id", req.LibraryID, "only_missing", onlyMissing, "overwrite", cfg.Overwrite)
	c.JSON(http.StatusAccepted, gin.H{"total": total})
}

// executeScrape 同步跑完一批刮削并完成全部收尾（调用方负责起 goroutine）。
// 同步形态是为了让计划任务能据此判定重叠与成败。
func (a *App) executeScrape(cfg scraper.Config, opts scraper.RunOptions) error {
	taskID := a.startTask("scrape")
	opts.OnProgress = a.updateScrapeProgress
	opts.OnItem = func(item scraper.ItemResult) {
		// 覆盖同名图片后必须显式失效 tag 缓存，否则客户端几分钟内仍拿旧图。
		if len(item.Images) > 0 {
			a.invalidateImageTag(item.Images...)
		}
		if item.Status != scraper.StatusSuccess {
			a.addScrapeFailure(displayItem(item))
		}
	}
	// NFO 通道只在这段刮削期间持有：收尾的整库扫描会自己重新申请，
	// 若仍持锁会被 claimNFO 判为「他人占用」而拒绝（互斥是按占用者类型判定的）。
	progress, err := func() (scraper.Progress, error) {
		defer a.releaseNFO("scrape")
		return scraper.New(cfg, a.db).Run(a.scrapeContext(), opts)
	}()
	a.finishTask(taskID, err)
	a.endScrape(err)

	// 批量路径逐部写盘时没有刷新索引，这里统一整库扫一次
	//（图片/NFO 可能落在多个库，整库扫描本身很快）。
	if err == nil && progress.Success > 0 {
		if _, scanErr := a.scanLibraries(0); scanErr != nil {
			slog.Warn("刮削后整库扫描失败", "error", scanErr)
		}
	}
	a.cache.Clear()
	slog.Info("刮削任务结束", "success", progress.Success, "skipped", progress.Skipped,
		"failed", progress.Failed, "total", progress.Total, "error", err)
	return err
}

// adminScrapeAvatars 启动演员头像任务（异步）。
func (a *App) adminScrapeAvatars(c *gin.Context) {
	var req struct {
		LibraryID int64 `json:"library_id"`
		Limit     int   `json:"limit"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
	}
	if value := c.Query("library_id"); value != "" && req.LibraryID == 0 {
		req.LibraryID, _ = strconv.ParseInt(value, 10, 64)
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置 MetaTube 地址或 token"})
		return
	}
	if owner := a.nfoBusyOwner(); owner != "" {
		c.JSON(http.StatusConflict, gin.H{"error": owner + "正在进行中，请稍后再试"})
		return
	}
	names, err := a.db.ActorsMissingAvatar(req.LibraryID, req.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(names) == 0 {
		c.JSON(http.StatusOK, gin.H{"total": 0, "message": "没有缺头像的演员（头像任务只补缺失，是幂等的）"})
		return
	}
	if !a.startScrapeTask(c, "scrape_avatars", len(names)) {
		return
	}
	go a.executeScrapeAvatars(cfg, req.LibraryID, req.Limit)
	slog.Info("演员头像任务已启动", "actors", len(names), "library_id", req.LibraryID)
	c.JSON(http.StatusAccepted, gin.H{"total": len(names)})
}

// executeScrapeAvatars 同步跑完头像任务并收尾（与影片刮削同样的同步形态）。
func (a *App) executeScrapeAvatars(cfg scraper.Config, libraryID int64, limit int) error {
	taskID := a.startTask("scrape_avatars")
	progress, err := func() (scraper.Progress, error) {
		defer a.releaseNFO("scrape_avatars")
		return scraper.New(cfg, a.db).RunAvatars(a.scrapeContext(), libraryID, limit,
			a.updateScrapeProgress, func(result scraper.AvatarResult) {
				if result.Status != scraper.StatusSuccess {
					a.addScrapeFailure("演员 " + result.Actor + "：" + result.Reason)
				}
			})
	}()
	a.finishTask(taskID, err)
	a.endScrape(err)
	a.cache.Clear()
	slog.Info("演员头像任务结束", "success", progress.Success, "skipped", progress.Skipped,
		"failed", progress.Failed, "total", progress.Total, "error", err)
	return err
}

// displayItem 把单条刮削结果格式化成可读的一行（进失败样例列表）。
func displayItem(item scraper.ItemResult) string {
	prefix := "失败"
	if item.Status == scraper.StatusSkipped {
		prefix = "跳过"
	}
	// 待人工确认的原因已经带了前缀，避免叠加成「跳过 [待人工确认]」。
	reason := strings.TrimPrefix(item.Reason, store.ScrapeConfirmPrefix)
	return prefix + " " + item.Title + "：" + reason
}

// —— 单条手动刮削：预览 → 确认 ——

func (a *App) adminScrapePreview(c *gin.Context) {
	movie, ok := a.movieParam(c)
	if !ok {
		return
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置 MetaTube 地址或 token"})
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	preview, err := scraper.New(cfg, a.db).Preview(ctx, movie)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
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
		Provider string `json:"provider"`
		ID       string `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Provider == "" || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider 与 id 必填"})
		return
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置 MetaTube 地址或 token"})
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	client := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
	info, err := client.MovieInfo(ctx, req.Provider, req.ID, true)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, a.buildInspect(cfg, movie, info))
}

// adminScrapeConfirm 确认写入（无状态预览：按 provider:id 重新取详情后落盘）。
func (a *App) adminScrapeConfirm(c *gin.Context) {
	movie, ok := a.movieParam(c)
	if !ok {
		return
	}
	var req struct {
		Provider  string `json:"provider"`
		ID        string `json:"id"`
		Overwrite *bool  `json:"overwrite"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Provider == "" || req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider 与 id 必填"})
		return
	}
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置 MetaTube 地址或 token"})
		return
	}
	if !a.claimNFO("scrape") {
		c.JSON(http.StatusConflict, gin.H{"error": a.nfoBusyOwner() + "正在进行中，请稍后再试"})
		return
	}
	defer a.releaseNFO("scrape")
	overwrite := cfg.Overwrite
	if req.Overwrite != nil {
		overwrite = *req.Overwrite
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	result, err := scraper.New(cfg, a.db).ScrapeOneMovie(ctx, movie, req.Provider, req.ID, overwrite)
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
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	slog.Info("单条刮削完成", "movie_id", movie.ID, "provider", req.Provider, "id", req.ID,
		"images", result.ImageCount, "overwrite", overwrite)
	c.JSON(http.StatusOK, gin.H{
		"status": "success", "provider": result.Provider, "number": result.Number,
		"title": result.Title, "images": result.ImageCount, "warnings": result.ImageWarn,
	})
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
	case "primary", "thumb", "backdrop":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported image kind"})
		return
	}
	provider, id := c.Query("provider"), c.Query("id")
	if provider == "" || id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider 与 id 必填"})
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	client := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
	data, err := client.Download(ctx, client.ImageURL(kind, provider, id, 70, ""), 8<<20)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	// 上游给的是 JPEG；原样转发，浏览器直接能显示，无需转码。
	c.Data(http.StatusOK, "image/jpeg", data)
}

// movieParam 解析并校验路径里的影片 id。
func (a *App) movieParam(c *gin.Context) (store.Movie, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return store.Movie{}, false
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
// 返回 false 时已写入 HTTP 错误响应。
func (a *App) startScrapeTask(c *gin.Context, kind string, total int) bool {
	if owner := a.nfoBusyOwner(); owner != "" {
		c.JSON(http.StatusConflict, gin.H{"error": owner + "正在进行中，请稍后再试"})
		return false
	}
	if !a.beginScrape(kind, total) {
		c.JSON(http.StatusConflict, gin.H{"error": errScrapeBusy.Error()})
		return false
	}
	if !a.claimNFO(kind) {
		a.endScrape(nil)
		c.JSON(http.StatusConflict, gin.H{"error": a.nfoBusyOwner() + "正在进行中，请稍后再试"})
		return false
	}
	return true
}
