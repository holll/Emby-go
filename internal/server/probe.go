package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/nfo"
	"emby-go/internal/probe"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

// 媒体信息探测：调用系统 ffprobe 读取影片真实技术参数并写回 NFO。
//
// 与扫库（scanner）职责分离——扫库只负责按磁盘现状入库，探测只负责获取媒体信息，
// 两者互不调用。探测不写索引库：NFO 是元数据真源，落盘后由下一次扫库带回库中。
//
// 探测是全库全量行为：默认只处理「NFO 里标记的探测版本低于 probeVersion」的条目，
// 已经探测过的一律跳过（只读一遍 NFO，不发 ffprobe 请求），因此反复触发全库探测是廉价的。
const maxProbeFailures = 20

// probeVersion 探测逻辑的版本号，写入 NFO 的 <fileinfo><probeversion>。
//
// 它解决「怎么算已探测」这个判定问题——不能用「有无 <streamdetails>」：
// 刮削器同样会写 streamdetails（且常不完整，缺位深/色彩特性/体积），
// 按有无判定会把这些条目永久跳过、拿不到补齐。
//
// 提升此版本的时机：探测新增了会改变 NFO 内容的字段时——
// 旧条目会在下一次全库探测中自动被重探，无需手工清理。
const probeVersion = 1

const (
	// maxProbeFailureLogs 完整打印失败详情的条数上限——失败可能多达数千条，
	// 逐条打全量日志会刷爆日志文件；之后改为按数量级汇总输出。
	maxProbeFailureLogs = 20
	// maxProbeConsecutiveFailures 连续失败达到该数量即判定为环境性故障并中止任务。
	// 库里零散的失效链接不会连续出现这么多，故正常情况下不会误触发；
	// 而 ffprobe 不可用、媒体库挂载异常这类问题会在几十毫秒内连续失败数千次，
	// 必须尽早中止，否则白跑一轮全量扫描。
	maxProbeConsecutiveFailures = 20
)

// probeStatus 探测任务的进度快照，供管理端轮询。
type probeStatus struct {
	Running    bool     `json:"running"`
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
	Aborted    bool     `json:"aborted,omitempty"` // 因连续失败被主动中止（区别于人工取消）
	Failures   []string `json:"failures,omitempty"`

	// 以下为内部状态，不对外输出。
	consecutiveFailures int `json:"-"` // 连续失败计数，成功即归零
	failureLogged       int `json:"-"` // 已完整输出日志的失败条数（限流用）
}

// probeRequest 探测任务入参；零值即「全部媒体库、仅待探测项、不强制覆盖」。
type probeRequest struct {
	LibraryID   int64  `json:"library_id"`
	Status      string `json:"status"`
	OnlyMissing *bool  `json:"only_missing"`
	Limit       int    `json:"limit"`
}

// probeTarget 一个待探测的媒体文件（一个 .strm）。
//
// 探测单位是「文件」而不是「影片」：分集影片（CD1/CD2…）的每个分段各有自己的
// .strm，必须逐个探测并各自留下 mediainfo.json，否则 CD2 之后的分段永远没有媒体信息。
type probeTarget struct {
	Movie   store.Movie
	Path    string // .strm 路径
	Primary bool   // 主文件：探测结果同时写入影片 NFO
	Index   int    // 分段序号（1 为主文件，2..n 对应 AdditionalParts）
}

// label 返回用于进度与失败信息展示的名称。
func (t probeTarget) label() string {
	if t.Primary {
		return t.Movie.Title
	}
	return t.Movie.Title + " - CD" + strconv.Itoa(t.Index)
}

// probeTargets 把影片列表展开成待探测文件列表：主文件 + 全部分段。
func probeTargets(movies []store.Movie) []probeTarget {
	out := make([]probeTarget, 0, len(movies))
	for _, movie := range movies {
		out = append(out, probeTarget{Movie: movie, Path: movie.SourcePath, Primary: true, Index: 1})
		for i, part := range movie.AdditionalParts {
			out = append(out, probeTarget{Movie: movie, Path: part, Index: i + 2})
		}
	}
	return out
}

// adminProbeMedia 启动媒体信息探测任务（异步执行，进度走 /probe/media/progress）。
func (a *App) adminProbeMedia(c *gin.Context) {
	var req probeRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
	}
	// 查询参数作为便捷入口（便于 curl 直接触发）。
	if value := c.Query("library_id"); value != "" && req.LibraryID == 0 {
		req.LibraryID, _ = strconv.ParseInt(value, 10, 64)
	}
	if value := c.Query("limit"); value != "" && req.Limit == 0 {
		req.Limit, _ = strconv.Atoi(value)
	}
	if req.Status == "" {
		req.Status = c.DefaultQuery("status", "success")
	}
	onlyMissing := true
	if req.OnlyMissing != nil {
		onlyMissing = *req.OnlyMissing
	} else if value := c.Query("only_missing"); value != "" {
		onlyMissing = value != "false" && value != "0"
	}

	if a.probing() {
		c.JSON(http.StatusConflict, gin.H{"error": errProbeBusy.Error()})
		return
	}
	// ffprobe 在任务启动前就解析并自检：缺 ffprobe 或它无法执行时直接给明确错误，
	// 而不是让成千上万条目逐个失败一遍（那既浪费时间又难归因）。
	ffprobe, err := probe.LookPath(a.cfg.FFProbePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := probe.Verify(a.rootCtx, ffprobe); err != nil {
		slog.Error("探测任务未启动：ffprobe 自检失败", "ffprobe", ffprobe, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	movies, err := a.db.MoviesForProbe(req.LibraryID, req.Status, req.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 展开成文件级任务：分集影片的每个分段 .strm 都要各自探测。
	targets := probeTargets(movies)
	if len(targets) == 0 {
		c.JSON(http.StatusOK, gin.H{"total": 0, "movies": 0, "message": "没有可探测的影片（探测需要影片已有 NFO）"})
		return
	}
	if !a.beginProbe(len(targets)) {
		c.JSON(http.StatusConflict, gin.H{"error": errProbeBusy.Error()})
		return
	}
	taskID := a.startTask("probe")
	go a.runProbe(taskID, ffprobe, targets, onlyMissing)
	slog.Info("媒体信息探测已启动", "task_id", taskID, "movies", len(movies), "files", len(targets), "only_missing", onlyMissing)
	c.JSON(http.StatusAccepted, gin.H{"task_id": taskID, "total": len(targets), "movies": len(movies)})
}

func (a *App) adminProbeMediaProgress(c *gin.Context) {
	a.probeTaskMu.RLock()
	status := a.probeStatus
	a.probeTaskMu.RUnlock()
	c.JSON(http.StatusOK, status)
}

// adminProbeMediaCancel 请求中止探测：已发出的 ffprobe 会被杀掉，未处理项直接结束。
func (a *App) adminProbeMediaCancel(c *gin.Context) {
	a.probeTaskMu.Lock()
	cancel := a.probeCancel
	running := a.probeStatus.Running
	a.probeTaskMu.Unlock()
	if !running || cancel == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "当前没有探测任务"})
		return
	}
	cancel()
	c.JSON(http.StatusOK, gin.H{"status": "cancelling"})
}

// adminProbeMediaItem 单条同步探测（管理端「探测」按钮）：立即返回探测到的参数。
func (a *App) adminProbeMediaItem(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if strings.TrimSpace(movie.NFOPath) == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "该影片没有 NFO，探测结果无处写入；请先完成元数据刮削"})
		return
	}
	ffprobe, err := probe.LookPath(a.cfg.FFProbePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()

	// 单条探测是管理端对某一片的显式动作，语义就是「现在重新探测」，
	// 因此不看缓存、直接联网；分集影片的全部 .strm 一并刷新。
	targets := probeTargets([]store.Movie{movie})
	files := make([]gin.H, 0, len(targets))
	var firstInfo probe.Info
	var failures []string
	for _, item := range targets {
		url, readErr := scanner.ReadSource(item.Path)
		if readErr != nil {
			failures = append(failures, item.label()+"：无法读取 .strm ("+readErr.Error()+")")
			continue
		}
		if !scanner.ValidHTTP(url) {
			failures = append(failures, item.label()+"：源不是 http(s) 地址")
			continue
		}
		result, runErr := probe.Run(ctx, ffprobe, url, a.cfg.ProbeTimeout())
		if runErr != nil {
			failures = append(failures, item.label()+"："+runErr.Error())
			continue
		}
		if item.Primary {
			if err = a.writeProbeNFO(movie, url, result.Info); err != nil {
				failures = append(failures, item.label()+"：写入 NFO 失败 ("+err.Error()+")")
				continue
			}
			firstInfo = result.Info
		}
		// 缓存写入失败不视为探测失败：NFO 已带版本与直链，去重不受影响。
		if err = saveMediaInfo(item.Path, url, result.Raw); err != nil {
			slog.Warn("写入 mediainfo.json 失败", "path", item.Path, "error", err)
		}
		files = append(files, gin.H{
			"kind":           map[bool]string{true: "primary", false: "part"}[item.Primary],
			"index":          item.Index,
			"path":           item.Path,
			"mediainfo_path": mediaInfoPath(item.Path),
			"info":           probeInfoJSON(result.Info),
		})
	}
	a.cache.Clear()
	if len(files) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "全部文件探测失败: " + strings.Join(failures, "；")})
		return
	}
	response := gin.H{
		"id":       movie.ID,
		"nfo_path": movie.NFOPath,
		"files":    files,
		"info":     probeInfoJSON(firstInfo), // 主文件信息，供前端摘要展示
	}
	if len(failures) > 0 {
		response["failures"] = failures
	}
	c.JSON(http.StatusOK, response)
}

// requestContext 让单条探测随客户端断开而取消，同时仍受本服务关闭信号约束。
func (a *App) requestContext(c *gin.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a.rootCtx)
	stop := context.AfterFunc(c.Request.Context(), cancel)
	return ctx, func() { stop(); cancel() }
}

// errProbeBusy 同一时刻只允许一个探测任务：并发探测会同时改写同一批 NFO 与进度。
var errProbeBusy = errors.New("媒体信息探测正在进行中")

// beginProbe 创建本轮任务的可取消 context（挂在常驻 rootCtx 之下）并置运行态。
func (a *App) beginProbe(total int) bool {
	a.probeTaskMu.Lock()
	defer a.probeTaskMu.Unlock()
	if a.probeStatus.Running {
		return false
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	a.probeCtx, a.probeCancel = ctx, cancel
	a.probeStatus = probeStatus{Running: true, Total: total, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	return true
}

// probing 仅用于提前拒绝，真正的互斥由 beginProbe 保证。
func (a *App) probing() bool {
	a.probeTaskMu.RLock()
	defer a.probeTaskMu.RUnlock()
	return a.probeStatus.Running
}

// runProbe 起固定数量的 worker 并行探测。ctx 取自 beginProbe 创建的常驻 context——
// 不能用发起请求的 context，否则 handler 一返回任务就被取消。
func (a *App) runProbe(taskID int64, ffprobe string, targets []probeTarget, onlyMissing bool) {
	a.probeTaskMu.RLock()
	ctx := a.probeCtx
	a.probeTaskMu.RUnlock()
	if ctx == nil {
		ctx = a.rootCtx
	}

	jobs := make(chan probeTarget)
	touched := make(chan int64, len(targets))
	var wg sync.WaitGroup
	for i := 0; i < a.cfg.ProbeWorkers(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 熔断/取消后不直接 return，而是继续排空 channel：
			// worker 一旦退出，还在阻塞投递的派发循环将永远等不到接收者而死锁。
			for target := range jobs {
				if ctx.Err() != nil || a.probeAborted() {
					continue
				}
				a.probeOne(ctx, ffprobe, target, onlyMissing, touched)
			}
		}()
	}
	// 派发时也提前检查，减少熔断后仍需排空的条目数。
	for _, target := range targets {
		if ctx.Err() != nil || a.probeAborted() {
			break
		}
		jobs <- target
	}
	close(jobs)
	wg.Wait()
	close(touched)

	// 探测只改 NFO，索引库字段（片长等）要等下一次扫库才更新；这里只清缓存，
	// 让已写入的流信息立刻对 API 生效。
	cleared := make(map[int64]struct{})
	for libraryID := range touched {
		if _, done := cleared[libraryID]; done {
			continue
		}
		cleared[libraryID] = struct{}{}
		_ = a.db.BumpVersion(libraryID)
	}
	a.cache.Clear()
	a.evictNFOStreams()

	a.probeTaskMu.Lock()
	status := &a.probeStatus
	aborted := status.consecutiveFailures >= maxProbeConsecutiveFailures
	// 熔断本身也会取消 context，这里要把它与「人工取消」区分开，否则会被误报为 cancelled。
	cancelled := ctx.Err() != nil && !aborted
	status.Running = false
	status.Cancelled = cancelled
	status.Aborted = aborted
	status.Current = ""
	status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if aborted {
		// 把「为什么中止」写进任务状态：管理端能直接看到，不必翻日志。
		status.Error = fmt.Sprintf("连续 %d 条探测失败，疑似探测环境异常（如 ffprobe 不可用或媒体库不可读），任务已提前中止；已处理 %d/%d，失败样例见 failures",
			status.consecutiveFailures, status.Done, status.Total)
	}
	success, skipped, failed, done := status.Success, status.Skipped, status.Failed, status.Done
	a.probeCancel = nil
	a.probeTaskMu.Unlock()

	if aborted {
		a.finishTask(taskID, errors.New("连续失败过多已中止"))
	} else {
		a.finishTask(taskID, nil)
	}
	slog.Info("媒体信息探测结束", "task_id", taskID, "success", success, "skipped", skipped,
		"failed", failed, "handled", done, "total", len(targets), "aborted", aborted, "cancelled", cancelled)
	if failed > 0 {
		// 汇总可能的原因分布，便于一眼定位是环境问题还是个别失效源。
		slog.Warn("媒体信息探测失败汇总", "failed", failed, "total", len(targets),
			"samples", a.probeFailureSamples())
	}
}

// probeAborted 报告是否已触发熔断（连续失败过多）。
func (a *App) probeAborted() bool {
	a.probeTaskMu.RLock()
	defer a.probeTaskMu.RUnlock()
	return a.probeStatus.consecutiveFailures >= maxProbeConsecutiveFailures
}

// probeFailureSamples 返回已留存的失败样例（供日志汇总）。
func (a *App) probeFailureSamples() []string {
	a.probeTaskMu.RLock()
	defer a.probeTaskMu.RUnlock()
	return append([]string(nil), a.probeStatus.Failures...)
}

// probeOne 处理单个媒体文件。判定顺序刻意从「最省」到「最贵」：
//  1. mediainfo.json（或主文件的 NFO 标记）显示已探测且直链未变 → 跳过，不发任何探测
//  2. 本地留有原始输出 → 重新映射回填，不联网
//  3. 其余 → 联网 ffprobe，写 mediainfo.json（主文件另写 NFO）
func (a *App) probeOne(ctx context.Context, ffprobe string, target probeTarget, onlyMissing bool, touched chan<- int64) {
	movie := target.Movie
	a.setProbeCurrent(target.label())
	defer a.markProbeDone()

	url, err := scanner.ReadSource(target.Path)
	if err != nil {
		a.probeFailure(movie, target.label()+"：读取 .strm 失败: "+err.Error())
		return
	}
	if !scanner.ValidHTTP(url) {
		a.probeFailure(movie, target.label()+"：源不是 http(s) 地址")
		return
	}
	if onlyMissing {
		file, hasCache := loadMediaInfo(target.Path, url)
		cacheCurrent := hasCache && file.ProbeVersion >= probeVersion
		// 主文件额外认 NFO 标记：兼容本次改动前就已探测、尚无 mediainfo.json 的条目，
		// 避免升级后触发一次全库重探。
		nfoCurrent := target.Primary && nfoProbeState(movie.NFOPath).isCurrent(url)
		switch {
		case cacheCurrent || nfoCurrent:
			// 数据已就绪。但主文件的 NFO 可能落后（外部改动/丢失），有本地原始数据就顺手回填，不联网。
			if target.Primary && !nfoCurrent && hasCache && a.backfillFromMediaInfo(target, file) {
				touched <- movie.LibraryID
				a.probeSuccess()
				return
			}
			a.probeSkipped()
			return
		case hasCache:
			// 缓存版本偏低（探测逻辑升级）：本地重新映射即可。
			if a.backfillFromMediaInfo(target, file) {
				touched <- movie.LibraryID
				a.probeSuccess()
				return
			}
			// 回填失败（缓存原始数据已不可解析）→ 落到下面的真实探测
		}
	}

	result, err := probe.Run(ctx, ffprobe, url, a.cfg.ProbeTimeout())
	if err != nil {
		a.probeFailure(movie, target.label()+"："+err.Error())
		return
	}
	// 主文件的 NFO 是权威产物，写入失败即整体失败。
	if target.Primary {
		if err := a.writeProbeNFO(movie, url, result.Info); err != nil {
			a.probeFailure(movie, target.label()+"：写入 NFO 失败: "+err.Error())
			return
		}
	}
	// mediainfo.json 是辅助缓存：写不进去只损失「本地回填」能力，
	// 去重仍由 NFO 标记（主文件）保证，不该因此把已成功的探测判为失败。
	if err := saveMediaInfo(target.Path, url, result.Raw); err != nil {
		slog.Warn("写入 mediainfo.json 失败（不影响去重，仅失去本地回填）", "path", target.Path, "error", err)
	}
	touched <- movie.LibraryID
	a.probeSuccess()
}

// probeSuccess / probeSkipped 记录非失败结果，并把连续失败计数归零——
// 连续失败计数是熔断的判据，中间只要有一条成功就说明环境是好的，必须重置。
func (a *App) probeSuccess() {
	a.countProbe(func(status *probeStatus) {
		status.Success++
		status.consecutiveFailures = 0
	})
}

func (a *App) probeSkipped() {
	a.countProbe(func(status *probeStatus) {
		status.Skipped++
		status.consecutiveFailures = 0
	})
}

// writeProbeNFO 把探测结果写入 NFO，并记录版本与直链（后者用于识别换源）。
func (a *App) writeProbeNFO(movie store.Movie, url string, info probe.Info) error {
	meta := nfo.FileInfoMeta{Size: info.SizeBytes, ProbeVersion: probeVersion, ProbeURL: url}
	if err := nfo.SaveFileInfo(movie.NFOPath, meta, streamDetailsFromProbe(info)); err != nil {
		return err
	}
	a.evictNFOStream(movie.NFOPath)
	return nil
}

// backfillFromMediaInfo 用 mediainfo.json 里的原始输出重写产物（不联网）。
// 返回是否成功；失败时调用方会回退到真实探测，因此缓存损坏不影响最终结果。
func (a *App) backfillFromMediaInfo(target probeTarget, file mediaInfoFile) bool {
	info, err := infoFromMediaInfo(file)
	if err != nil {
		// 缓存的原始数据已不可解析：删掉它，让下次走真实探测。
		removeMediaInfo(target.Path)
		return false
	}
	if target.Primary {
		if err := a.writeProbeNFO(target.Movie, file.Source, info); err != nil {
			return false
		}
	}
	// 缓存版本偏低时同步提升，避免每次都走这条回填路径；已是最新则不动它，
	// 免得平白刷新 probed_at 造成文件 churn。
	if file.ProbeVersion < probeVersion {
		_ = saveMediaInfo(target.Path, file.Source, file.FFProbe)
	}
	return true
}

// probeFailure 记录一次失败：累计计数、留存样例、输出日志，并追踪连续失败次数。
//
// 日志必须打——失败详情只回给管理端 API 的话，运维在服务日志里什么都看不到，
// 生产上出现「几千条全失败」时无从归因。这里对条数做限流，避免刷爆日志。
func (a *App) probeFailure(movie store.Movie, reason string) {
	a.probeTaskMu.Lock()
	status := &a.probeStatus
	status.Failed++
	status.consecutiveFailures++
	if len(status.Failures) < maxProbeFailures {
		// reason 由调用方带上「标题/分段名：」前缀，这里不再重复拼接。
		status.Failures = append(status.Failures, reason)
	}
	shouldLog := status.failureLogged < maxProbeFailureLogs
	if shouldLog {
		status.failureLogged++
	}
	total, consecutive := status.Failed, status.consecutiveFailures
	// 刚达到熔断阈值：顺手取消本轮的 context，把还在跑的 ffprobe 一并杀掉。
	// 否则任务要等这些进程各自超时（可长达 probe_timeout）才结束，白等一场。
	tripped := consecutive == maxProbeConsecutiveFailures && a.probeCancel != nil
	cancel := a.probeCancel
	a.probeTaskMu.Unlock()

	if tripped {
		slog.Warn("媒体信息探测触发熔断：连续失败过多，中止任务", "consecutive", consecutive, "failed", total, "reason", reason)
		cancel()
	}

	switch {
	case shouldLog:
		slog.Warn("媒体信息探测失败", "title", movie.Title, "strm", movie.SourcePath, "reason", reason, "failed", total)
	case total%500 == 0:
		// 长时间持续失败时给出进度级提示，避免日志完全静默。
		slog.Warn("媒体信息探测持续失败", "failed", total, "consecutive", consecutive, "last_reason", reason)
	}
}

func (a *App) setProbeCurrent(title string) {
	a.probeTaskMu.Lock()
	a.probeStatus.Current = title
	a.probeTaskMu.Unlock()
}

func (a *App) markProbeDone() {
	a.probeTaskMu.Lock()
	a.probeStatus.Done++
	a.probeTaskMu.Unlock()
}

func (a *App) countProbe(apply func(*probeStatus)) {
	a.probeTaskMu.Lock()
	apply(&a.probeStatus)
	a.probeTaskMu.Unlock()
}

// evictNFOStream 让单条影片的流信息缓存立即失效（下次请求重新解析刚写入的 NFO）。
func (a *App) evictNFOStream(path string) {
	a.nfoMu.Lock()
	delete(a.nfos, path)
	a.nfoMu.Unlock()
}

func (a *App) evictNFOStreams() {
	a.nfoMu.Lock()
	a.nfos = make(map[string]nfoCacheEntry)
	a.nfoMu.Unlock()
}

// probeState NFO 里记录的探测状态。
type probeState struct {
	Version int
	URL     string
}

// nfoProbeState 读出 NFO 记录的探测版本与探测时的直链；读不到时返回零值。
//
// 判定刻意只看我们自己的标记，不看 <streamdetails> 是否存在：
// 后者会把刮削器写入的不完整流信息误判为「已探测」，导致该补齐的条目被跳过。
// 探测失败时不写标记，因此失败条目会在下次全库探测中自动重试。
func nfoProbeState(path string) probeState {
	if strings.TrimSpace(path) == "" {
		return probeState{}
	}
	meta, err := nfo.Read(path)
	if err != nil || meta.FileInfo == nil {
		return probeState{}
	}
	return probeState{Version: meta.FileInfo.ProbeVersion, URL: strings.TrimSpace(meta.FileInfo.ProbeURL)}
}

// isCurrent 判断该状态对当前直链是否仍然有效。
// URL 为空表示条目由旧版本写入、未记录直链，此时只认版本号（避免升级后全库重探）。
func (s probeState) isCurrent(target string) bool {
	if s.Version < probeVersion {
		return false
	}
	return s.URL == "" || s.URL == target
}

// streamDetailsFromProbe 把探测结果映射为 NFO 的 streamdetails 结构。
func streamDetailsFromProbe(info probe.Info) *nfo.StreamDetails {
	details := &nfo.StreamDetails{}
	if video := info.Video; video != nil {
		minutes := 0
		if info.DurationSeconds > 0 {
			minutes = int((info.DurationSeconds + 30) / 60) // 四舍五入到分钟
		}
		details.Video = &nfo.VideoStream{
			Codec: video.Codec, CodecTag: video.CodecTag,
			Profile: video.Profile, Level: video.Level,
			PixelFormat: video.PixelFormat, BitDepth: video.BitDepth, RefFrames: video.RefFrames,
			ColorTransfer: video.ColorTransfer, ColorPrimaries: video.ColorPrimaries,
			ColorSpace: video.ColorSpace, ColorRange: video.ColorRange,
			Bitrate: video.Bitrate, Width: video.Width, Height: video.Height,
			AspectRatio: video.AspectRatio, Framerate: video.Framerate,
			Language: video.Language, ScanType: video.ScanType,
			DurationMinutes: minutes, DurationSeconds: info.DurationSeconds,
			Default: boolText(video.Default), Forced: boolText(video.Forced),
		}
	}
	if audio := info.Audio; audio != nil {
		details.Audio = &nfo.AudioStream{
			Codec: audio.Codec, CodecTag: audio.CodecTag, Profile: audio.Profile,
			ChannelLayout: audio.ChannelLayout, Bitrate: audio.Bitrate,
			Language: audio.Language, Channels: audio.Channels, SamplingRate: audio.SamplingRate,
			Default: boolText(audio.Default), Forced: boolText(audio.Forced),
		}
	}
	return details
}

// boolText 输出 Metatube 风格的 True/False（大小写与刮削器一致，便于人工比对）。
func boolText(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

// probeInfoJSON 供管理端单条探测回显。
func probeInfoJSON(info probe.Info) gin.H {
	out := gin.H{
		"duration_seconds": info.DurationSeconds,
		"size_bytes":       info.SizeBytes,
		"bitrate":          info.Bitrate,
		"format":           info.FormatName,
	}
	if video := info.Video; video != nil {
		out["video"] = gin.H{
			"codec": video.Codec, "codec_tag": video.CodecTag,
			"profile": video.Profile, "level": video.Level,
			"pixel_format": video.PixelFormat, "bit_depth": video.BitDepth, "ref_frames": video.RefFrames,
			"width": video.Width, "height": video.Height, "bitrate": video.Bitrate,
			"framerate": video.Framerate, "aspect_ratio": video.AspectRatio,
			"language": video.Language, "scan_type": video.ScanType,
			"default": video.Default, "forced": video.Forced,
		}
	}
	if audio := info.Audio; audio != nil {
		out["audio"] = gin.H{
			"codec": audio.Codec, "codec_tag": audio.CodecTag, "profile": audio.Profile,
			"channel_layout": audio.ChannelLayout, "bitrate": audio.Bitrate,
			"language": audio.Language, "channels": audio.Channels, "sampling_rate": audio.SamplingRate,
			"default": audio.Default, "forced": audio.Forced,
		}
	}
	return out
}
