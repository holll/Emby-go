package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/imageutil"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// Sprite generations have their own atomic index; they never change poster or NFO data.
type spriteRequest struct {
	MovieIDs  []int64 `json:"movie_ids"`
	Overwrite bool    `json:"overwrite"`
	intervals map[int64]int
	// next pages scheduled targets without retaining an unbounded ID list.
	next func(context.Context) (movieID int64, interval int, ok bool, err error)
}
type spriteStatus struct {
	InstanceID  string  `json:"instance_id"`
	TaskID      int64   `json:"task_id"`
	MovieIDs    []int64 `json:"movie_ids"`
	Running     bool    `json:"running"`
	Total       int     `json:"total"`
	Done        int     `json:"done"`
	Success     int     `json:"success"`
	Skipped     int     `json:"skipped"`
	Failed      int     `json:"failed"`
	Current     string  `json:"current"`
	FramesDone  int     `json:"frames_done"`
	FramesTotal int     `json:"frames_total"`
	Cancelled   bool    `json:"cancelled"`
	Error       string  `json:"error,omitempty"`
	FinishedAt  string  `json:"finished_at,omitempty"`
}
type spriteState struct {
	mu      sync.RWMutex
	status  spriteStatus
	cancel  context.CancelFunc
	run     func(context.Context, *store.Store, artwork.SpriteRequest, artwork.SpriteOptions) (artwork.SpriteResult, error)
	readSem chan struct{}
}

func decodeSpriteRequest(c *gin.Context) (spriteRequest, error) {
	var req spriteRequest
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil {
		return req, err
	}
	for k := range raw {
		if k != "movie_ids" && k != "overwrite" {
			return req, errors.New("unknown sprite field")
		}
	}
	if json.Unmarshal(data, &req) != nil || len(req.MovieIDs) == 0 || len(req.MovieIDs) > 20 {
		return req, errors.New("provide 1 to 20 movie_ids")
	}
	seen := make(map[int64]bool)
	for _, id := range req.MovieIDs {
		if id <= 0 || seen[id] {
			return req, errors.New("movie_ids must be positive and distinct")
		}
		seen[id] = true
	}
	return req, nil
}
func (a *App) spriteRootDir() string {
	path, _ := filepath.Abs(a.cfg.DBPath)
	return filepath.Join(filepath.Dir(path), "video-previews")
}
func (a *App) spriteOptions() (artwork.SpriteOptions, error) {
	opts, err := a.artworkOptions("screenshot")
	return artwork.SpriteOptions{RootDir: a.spriteRootDir(), FFmpegPath: opts.FFmpegPath, FFprobePath: opts.FFprobePath, Timeout: 5 * time.Minute}, err
}
func (a *App) adminSpriteCapabilities(c *gin.Context) {
	if !a.admitAdminOperation(c) {
		return
	}
	_, err := a.spriteOptions()
	reason := ""
	if err != nil {
		reason = "ffmpeg 或 ffprobe 不可用"
	}
	interval := store.DefaultSpriteIntervalSeconds
	if values, supplied := c.Request.URL.Query()["movie_id"]; supplied {
		if len(values) != 1 {
			apiError(c, 400, "invalid movie_id")
			return
		}
		id, e := strconv.ParseInt(values[0], 10, 64)
		if e != nil || id <= 0 {
			apiError(c, 400, "invalid movie_id")
			return
		}
		m, _, e := a.db.SpriteSource(c.Request.Context(), id)
		if e != nil {
			apiError(c, 404, "sprite target not found")
			return
		}
		interval, e = a.db.LibrarySpriteInterval(c.Request.Context(), m.LibraryID)
		if e != nil {
			apiError(c, 503, "library preview settings unavailable")
			return
		}
	}
	c.JSON(200, gin.H{"enabled": err == nil, "reason": reason, "interval_seconds": interval, "min_interval_seconds": store.MinSpriteIntervalSeconds, "max_interval_seconds": store.MaxSpriteIntervalSeconds, "max_frames": 600, "width": 320, "height": 180})
}
func (a *App) adminSpriteRun(c *gin.Context) {
	req, err := decodeSpriteRequest(c)
	if err != nil {
		apiError(c, 400, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	opts, err := a.spriteOptions()
	if err != nil {
		apiError(c, 503, "ffmpeg 或 ffprobe 不可用")
		return
	}
	req.intervals = make(map[int64]int, len(req.MovieIDs))
	libraryIntervals := make(map[int64]int)
	for _, id := range req.MovieIDs {
		m, _, e := a.db.SpriteSource(c.Request.Context(), id)
		if e != nil {
			apiError(c, 404, "sprite target not found")
			return
		}
		if !strings.EqualFold(filepath.Ext(m.SourcePath), ".strm") {
			apiError(c, 400, "sprite target must be STRM")
			return
		}
		interval, exists := libraryIntervals[m.LibraryID]
		if !exists {
			interval, e = a.db.LibrarySpriteInterval(c.Request.Context(), m.LibraryID)
			if e != nil {
				apiError(c, 503, "library preview settings unavailable")
				return
			}
			libraryIntervals[m.LibraryID] = interval
		}
		req.intervals[id] = interval
	}
	a.sprites.mu.Lock()
	if a.rootCtx.Err() != nil {
		a.sprites.mu.Unlock()
		apiError(c, 503, "server shutting down")
		return
	}
	// 同 artwork：在已持有的 sprites.mu 内、置运行态之前登记 worker。
	release, err := a.beginWorker()
	if err != nil {
		a.sprites.mu.Unlock()
		apiError(c, 503, err.Error())
		return
	}
	if a.sprites.status.Running || !a.claimNFO("sprites") {
		a.sprites.mu.Unlock()
		release()
		apiError(c, 409, "媒体维护任务正在进行中")
		return
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	id := a.startTask("video_sprites")
	accepted := spriteStatus{InstanceID: a.instanceID, TaskID: id, MovieIDs: append([]int64{}, req.MovieIDs...), Running: true, Total: len(req.MovieIDs)}
	a.sprites.status = accepted
	a.sprites.cancel = cancel
	a.sprites.mu.Unlock()
	go func() { defer release(); a.executeSprites(ctx, id, req, opts) }()
	c.JSON(202, accepted)
}

// spriteFailLogLimit 逐影片失败日志只记录前若干条：大规模失败时不刷屏，
// 也不为每条失败额外分配 Attr；剩余数量与首因由结尾的汇总行给出。
const spriteFailLogLimit = 10

// errSpriteNoOutput 生成器既未报错、也未返回跳过或变更：结果自相矛盾。
// 计为失败并留下原因，避免只看到失败计数、看不到为什么。
var errSpriteNoOutput = errors.New("生成器既未报错也未产生新版本")

func (a *App) executeSprites(ctx context.Context, id int64, req spriteRequest, opts artwork.SpriteOptions) (runErr error) {
	started := time.Now()
	// 诊断信息只在本次执行的内存里累计，逐影片路径是常数开销。
	var firstErr error // 首个失败原因（含影片 id），只赋值一次
	var listErr error  // 目标分页读取失败：底层原因要留在任务行上
	done, failed, logged, success, skipped := 0, 0, 0, 0, 0
	defer func() {
		a.sprites.mu.Lock()
		s := &a.sprites.status
		s.Running = false
		s.Current = ""
		s.Cancelled = ctx.Err() != nil
		s.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		var taskErr error
		switch {
		case s.Cancelled:
			s.Error = "任务已取消"
			taskErr = errors.New(s.Error)
		case listErr != nil:
			// 目标读取失败曾经只写固定文案，底层 err 被丢掉；任务行是管理端
			// 唯一可见的失败说明，这里必须把原因带上。
			s.Error = "视频预览任务目标读取失败: " + listErr.Error()
			taskErr = fmt.Errorf("视频预览任务目标读取失败: %w", listErr)
		case s.Failed > 0:
			// 逐影片原因只进服务日志：错误文本里可能含带凭据的 STRM URL，
			// 不能落到管理端可见的任务行与计划任务「上次结果」。
			s.Error = "部分视频预览生成失败，请检查可信源及服务配置"
			taskErr = errors.New(s.Error)
		}
		if s.Cancelled {
			runErr = context.Canceled
		} else {
			runErr = taskErr
		}
		cancelled, taskID := s.Cancelled, s.TaskID
		if a.sprites.cancel != nil {
			a.sprites.cancel()
		}
		a.sprites.cancel = nil
		a.finishTask(id, taskErr)
		a.releaseNFO("sprites")
		a.sprites.mu.Unlock()
		// 汇总日志放在解锁之后：写日志不该占着状态锁，也不影响已经落定的状态。
		elapsed := time.Since(started).Round(time.Millisecond).String()
		switch {
		case listErr != nil:
			slog.Error("视频预览任务中止：目标读取失败", "task_id", taskID, "instance_id", a.instanceID,
				"done", done, "failed", failed, "elapsed", elapsed, "error", listErr)
		case cancelled:
			slog.Info("视频预览任务已取消", "task_id", taskID, "done", done,
				"success", success, "skipped", skipped, "failed", failed, "elapsed", elapsed)
		case failed > 0:
			slog.Warn("视频预览任务结束：部分影片生成失败", "task_id", taskID, "instance_id", a.instanceID,
				"done", done, "success", success, "skipped", skipped, "failed", failed,
				"logged", logged, "suppressed", failed-logged, "elapsed", elapsed, "first_error", firstErr)
		default:
			// 成功路径也必须有一条汇总：否则「已开始」之后仍然什么都没有，
			// 用户只能靠任务行的时间戳猜它到底生成了多少张。
			slog.Info("视频预览任务结束", "task_id", taskID, "instance_id", a.instanceID,
				"done", done, "success", success, "skipped", skipped, "failed", failed, "elapsed", elapsed)
		}
	}()
	run := a.sprites.run
	if run == nil {
		run = artwork.GenerateSprite
	}
	index := 0
	next := req.next
	if next == nil {
		next = func(context.Context) (int64, int, bool, error) {
			if index == len(req.MovieIDs) {
				return 0, 0, false, nil
			}
			movieID := req.MovieIDs[index]
			index++
			return movieID, req.intervals[movieID], true, nil
		}
	}
	for ctx.Err() == nil {
		movieID, interval, ok, err := next(ctx)
		if err != nil {
			// 底层 err 必须一路留到任务行与汇总日志，而不是只留一句固定文案。
			listErr = err
			break
		}
		if !ok {
			break
		}
		a.sprites.mu.Lock()
		a.sprites.status.Current = strconv.FormatInt(movieID, 10)
		a.sprites.status.FramesDone = 0
		a.sprites.status.FramesTotal = 0
		a.sprites.mu.Unlock()
		opts.Progress = func(done, total int) {
			a.sprites.mu.Lock()
			if a.sprites.status.TaskID == id && total >= 0 && done >= 0 && done <= total {
				a.sprites.status.FramesDone = done
				a.sprites.status.FramesTotal = total
			}
			a.sprites.mu.Unlock()
		}
		result, err := run(ctx, a.db, artwork.SpriteRequest{MovieID: movieID, IntervalMs: int64(interval) * 1000, Overwrite: req.Overwrite}, opts)
		var cause error
		shouldLog := false
		a.sprites.mu.Lock()
		s := &a.sprites.status
		s.Done++
		done++
		switch {
		case err != nil:
			s.Failed++
			cause = err
		case result.Skipped:
			s.Skipped++
			skipped++
		case result.Changed:
			s.Success++
			success++
		default:
			s.Failed++
			cause = errSpriteNoOutput
		}
		if cause != nil {
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("影片 %d: %w", movieID, cause)
			}
			// 逐影片日志限量：前 spriteFailLogLimit 条带原始原因，其余计入汇总的 suppressed。
			if logged < spriteFailLogLimit {
				logged++
				shouldLog = true
			}
		}
		a.sprites.mu.Unlock()
		if shouldLog {
			msg := "视频预览生成失败"
			if err == nil {
				msg = "视频预览生成未产生新版本"
			}
			slog.Warn(msg, "task_id", id, "movie_id", movieID, "interval_seconds", interval, "error", cause)
		}
	}
	return nil
}
func (a *App) adminSpriteStatus(c *gin.Context) {
	q := c.Request.URL.Query()
	if len(q["task_id"]) > 1 || len(q["instance_id"]) > 1 {
		apiError(c, 400, "ambiguous task identity")
		return
	}
	var id int64
	if values, exists := q["task_id"]; exists {
		var err error
		id, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || id <= 0 || c.Query("instance_id") == "" {
			apiError(c, 400, "task_id and instance_id required")
			return
		}
	} else if _, exists := q["instance_id"]; exists {
		apiError(c, 400, "task_id required")
		return
	}
	a.sprites.mu.RLock()
	s := a.sprites.status
	s.MovieIDs = append([]int64{}, s.MovieIDs...)
	a.sprites.mu.RUnlock()
	if id != 0 && (s.TaskID != id || c.Query("instance_id") != a.instanceID) {
		apiError(c, 404, "sprite task not found")
		return
	}
	s.InstanceID = a.instanceID
	c.JSON(200, s)
}
func (a *App) adminSpriteCancel(c *gin.Context) {
	var req struct {
		TaskID     int64  `json:"task_id"`
		InstanceID string `json:"instance_id"`
	}
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil || len(raw) != 2 || raw["task_id"] == nil || raw["instance_id"] == nil || json.Unmarshal(data, &req) != nil || req.TaskID <= 0 || req.InstanceID == "" || len(req.InstanceID) > 256 {
		apiError(c, 400, "invalid task identity")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	a.sprites.mu.Lock()
	defer a.sprites.mu.Unlock()
	if req.InstanceID != a.instanceID || req.TaskID != a.sprites.status.TaskID {
		apiError(c, 404, "sprite task not found")
		return
	}
	if a.sprites.status.Running && a.sprites.cancel != nil {
		a.sprites.cancel()
	}
	c.JSON(200, gin.H{"instance_id": a.instanceID, "task_id": req.TaskID, "ok": true})
}

// All preview bytes require a live account and current library grant, unlike public posters.
func (a *App) spriteTarget(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(c, 404, "preview not found")
		return 0, false
	}
	m, err := a.db.Movie(id)
	if err != nil || !m.IsVisible() || !a.canViewMovie(c, m) {
		apiError(c, 404, "preview not found")
		return 0, false
	}
	return id, true
}
func (a *App) webSpriteManifest(c *gin.Context) {
	id, ok := a.spriteTarget(c)
	if !ok {
		return
	}
	m, err := artwork.GetSpriteManifest(c.Request.Context(), a.db, id, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	sheets := make([]gin.H, 0, len(m.Sheets))
	for i, s := range m.Sheets {
		sheets = append(sheets, gin.H{"url": "/api/items/" + strconv.FormatInt(id, 10) + "/sprites/sheets/" + strconv.Itoa(i) + "?generation=" + m.Generation, "width": s.Width, "height": s.Height, "count": s.Count})
	}
	c.JSON(200, gin.H{"version": 1, "generation": m.Generation, "width": m.Width, "height": m.Height, "columns": m.Cols, "rows": m.Rows, "count": m.Count, "interval_ms": m.IntervalMs, "duration_ms": m.DurationMs, "sheet_count": len(m.Sheets), "sheets": sheets})
}

// spriteReadSlot 申请一个图片读取槽位，返回归还函数。
// 归还函数闭包捕获本次拿到的 sem，而不是在释放时再读 a.sprites.readSem——
// 后者是无锁读共享字段（数据竞争），且错误返回路径上可能把槽位还给了另一实例。
func (a *App) spriteReadSlot(c *gin.Context) (release func(), ok bool) {
	a.sprites.mu.Lock()
	if a.sprites.readSem == nil {
		a.sprites.readSem = make(chan struct{}, 2)
	}
	sem := a.sprites.readSem
	a.sprites.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		apiError(c, 503, "preview busy")
		return nil, false
	}
}
func (a *App) webSpriteSheet(c *gin.Context) {
	id, ok := a.spriteTarget(c)
	if !ok {
		return
	}
	page, err := strconv.Atoi(c.Param("page"))
	q := c.Request.URL.Query()
	if err != nil || page < 0 || page >= 6 || len(q["generation"]) != 1 {
		apiError(c, 400, "invalid sprite identity")
		return
	}
	release, ok := a.spriteReadSlot(c)
	if !ok {
		return
	}
	defer release()
	data, err := artwork.ReadSpriteSheet(c.Request.Context(), a.db, id, c.Query("generation"), page, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, "image/jpeg", data)
}

// Native clients vary query casing. Reject duplicate aliases instead of letting
// different layers select different source identities or timestamps.
func nativeSpriteParam(c *gin.Context, key string) (string, bool, bool) {
	var values []string
	for k, v := range c.Request.URL.Query() {
		if strings.EqualFold(k, key) {
			values = append(values, v...)
		}
	}
	if len(values) > 1 {
		apiError(c, 400, "ambiguous preview parameter")
		return "", true, false
	}
	if len(values) == 0 {
		return "", false, true
	}
	return values[0], true, true
}

// There is one BIF rendition (320px). Width is a preference, not a new profile.
func nativeSpriteQuery(c *gin.Context, id int64) bool {
	width, present, ok := nativeSpriteParam(c, "Width")
	if !ok {
		return false
	}
	if present {
		n, err := strconv.Atoi(width)
		if err != nil || n < 1 || n > 4096 {
			apiError(c, 400, "invalid preview width")
			return false
		}
	}
	source, _, ok := nativeSpriteParam(c, "MediaSourceId")
	if !ok {
		return false
	}
	if source != "" && source != "mediasource_"+strconv.FormatInt(id, 10) {
		apiError(c, 404, "preview source not found")
		return false
	}
	return true
}
func (a *App) spriteThumbnailSet(c *gin.Context) {
	id, ok := a.spriteTarget(c)
	if !ok || !nativeSpriteQuery(c, id) {
		return
	}
	m, err := artwork.GetSpriteManifest(c.Request.Context(), a.db, id, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	thumbnails := make([]gin.H, 0, m.Count)
	for i := 0; i < m.Count; i++ {
		thumbnails = append(thumbnails, gin.H{"PositionTicks": int64(i) * m.IntervalMs * 10000, "ImageTag": m.Generation})
	}
	c.JSON(200, gin.H{"AspectRatio": float64(m.Width) / float64(m.Height), "Thumbnails": thumbnails})
}
func (a *App) spriteThumbnail(c *gin.Context) {
	noStore(c)
	id, ok := a.spriteTarget(c)
	if !ok || !nativeSpriteQuery(c, id) {
		return
	}
	position, present, valid := nativeSpriteParam(c, "PositionTicks")
	if !valid {
		return
	}
	if !present {
		apiError(c, 400, "PositionTicks required")
		return
	}
	ticks, err := strconv.ParseInt(position, 10, 64)
	if err != nil || ticks < 0 {
		apiError(c, 400, "invalid PositionTicks")
		return
	}
	release, ok := a.spriteReadSlot(c)
	if !ok {
		return
	}
	defer release()
	m, err := artwork.GetSpriteManifest(c.Request.Context(), a.db, id, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	if ticks/10000 >= m.DurationMs {
		apiError(c, 404, "preview position not found")
		return
	}
	tag, present, valid := nativeSpriteParam(c, "tag")
	if !valid {
		return
	}
	if present && tag != m.Generation {
		apiError(c, 404, "preview generation not found")
		return
	}
	frame := int((ticks / 10000) / m.IntervalMs)
	data, err := artwork.ReadSpriteFrame(c.Request.Context(), a.db, id, m.Generation, frame, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	width, height, quality := imageResizeParams(c)
	if width > 0 || height > 0 {
		img, e := jpeg.Decode(bytes.NewReader(data))
		if e != nil {
			apiError(c, 404, "preview not found")
			return
		}
		scaled := imageutil.Downscale(img, width, height)
		if scaled != img || quality > 0 {
			if quality == 0 {
				quality = 85
			}
			var out bytes.Buffer
			if jpeg.Encode(&out, scaled, &jpeg.Options{Quality: quality}) != nil {
				apiError(c, 503, "preview unavailable")
				return
			}
			data = out.Bytes()
		}
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, "image/jpeg", data)
}
func (a *App) spriteBIF(c *gin.Context) {
	id, ok := a.spriteTarget(c)
	if !ok || !nativeSpriteQuery(c, id) {
		return
	}
	release, ok := a.spriteReadSlot(c)
	if !ok {
		return
	}
	defer release()
	m, err := artwork.GetSpriteManifest(c.Request.Context(), a.db, id, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	data, err := artwork.ReadSpriteBIF(c.Request.Context(), a.db, id, m.Generation, a.spriteRootDir())
	if err != nil {
		apiError(c, 404, "preview not found")
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, "index.bif", time.Time{}, bytes.NewReader(data))
}
