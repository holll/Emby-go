package server

import (
	"database/sql"
	"emby-go/internal/imageutil"
	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type task struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (a *App) adminLibraries(c *gin.Context) {
	v, e := a.db.Libraries()
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, gin.H{"items": v, "total": len(v)})
}

// adminDeleteLibrary 删除媒体库及其影片索引（不删磁盘文件），随后清缓存。
func (a *App) adminDeleteLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if err = a.db.DeleteLibrary(id); err != nil {
		if store.NotFound(err) {
			c.JSON(404, gin.H{"error": "library not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_ = a.db.BumpVersion(id)
	a.cache.Clear()
	slog.Info("删除媒体库", "library_id", id)
	c.Status(http.StatusNoContent)
}

func (a *App) adminAddLibrary(c *gin.Context) {
	var req struct{ Name, Path string }
	if c.ShouldBindJSON(&req) != nil || req.Name == "" || req.Path == "" {
		c.JSON(400, gin.H{"error": "name and path required"})
		return
	}
	v, e := a.db.AddLibrary(req.Name, req.Path)
	if e != nil {
		c.JSON(400, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, v)
}

func (a *App) startTask(kind string) int64 {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	a.nextTaskID++
	id := a.nextTaskID
	a.tasks = append([]task{{ID: id, Type: kind, Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339)}}, a.tasks...)
	if len(a.tasks) > 100 {
		a.tasks = a.tasks[:100]
	}
	slog.Info("任务开始", "task_id", id, "type", kind)
	return id
}

func (a *App) finishTask(id int64, err error) {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	for i := range a.tasks {
		if a.tasks[i].ID != id {
			continue
		}
		a.tasks[i].EndedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			a.tasks[i].Status, a.tasks[i].Error = "failed", err.Error()
			slog.Error("任务失败", "task_id", id, "type", a.tasks[i].Type, "error", err)
			return
		}
		a.tasks[i].Status = "success"
		slog.Info("任务成功", "task_id", id, "type", a.tasks[i].Type)
		return
	}
}

// finishTaskBusy 把任务记为「跳过」：上一轮尚未结束，本次不执行也不算失败。
func (a *App) finishTaskBusy(id int64, reason string) {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	for i := range a.tasks {
		if a.tasks[i].ID != id {
			continue
		}
		a.tasks[i].EndedAt = time.Now().UTC().Format(time.RFC3339)
		a.tasks[i].Status, a.tasks[i].Error = "skipped", reason
		slog.Info("任务跳过", "task_id", id, "type", a.tasks[i].Type, "reason", reason)
		return
	}
}

// errScanBusy 同一时刻只允许一个扫描任务，避免并发写库与进度互相覆盖。
var errScanBusy = errors.New("扫描正在进行中")

// beginScan / updateScanProgress / endScan 维护管理端可轮询的扫描进度。
// beginScan 返回 false 表示已有扫描在跑，或有探测/刮削占用着同一批 NFO。
func (a *App) beginScan(libraries int) bool {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.scanStatus.Running {
		return false
	}
	// 探测/刮削也在写同一批 NFO，且整库扫描的 DeleteMissingSources 需要独占遍历。
	if !a.claimNFO("scan") {
		return false
	}
	a.scanStatus = scanStatus{Running: true, Libraries: libraries, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	return true
}

func (a *App) setScanLibrary(index int, library store.Library) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.scanStatus.LibraryIndex = index
	a.scanStatus.LibraryName = library.Name
	a.scanStatus.Total, a.scanStatus.Done, a.scanStatus.Current = 0, 0, ""
}

func (a *App) updateScanProgress(p scanner.Progress) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.scanStatus.LibraryName = p.LibraryName
	a.scanStatus.Total, a.scanStatus.Done, a.scanStatus.Current = p.Total, p.Done, p.Current
	a.scanStatus.Success, a.scanStatus.Pending = p.Result.Success, p.Result.Pending
	a.scanStatus.Incompatible, a.scanStatus.Failed = p.Result.Incompatible, p.Result.Failed
}

func (a *App) endScan(err error) {
	a.scanMu.Lock()
	a.scanStatus.Running = false
	a.scanStatus.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		a.scanStatus.Error = err.Error()
	} else {
		a.scanStatus.Done = a.scanStatus.Total
	}
	a.scanMu.Unlock()
	// 释放 NFO 通道放在 scanMu 之外：两把锁的获取顺序统一为
	// {scanMu|probeTaskMu|scrapeTaskMu} → nfoGate，混进同一临界区会形成反向依赖。
	a.releaseNFO("scan")
}

func (a *App) adminScanProgress(c *gin.Context) {
	a.scanMu.RLock()
	status := a.scanStatus
	a.scanMu.RUnlock()
	c.JSON(200, status)
}

// scanLibraries 扫描指定媒体库（libraryID=0 表示全部）。扫描即重建：
// 逐文件 upsert，收尾按磁盘现状删除失效索引行，再清缓存。
func (a *App) scanLibraries(libraryID int64) (scanner.Result, error) {
	libraries, err := a.db.Libraries()
	if err != nil {
		return scanner.Result{}, err
	}
	selected := make([]store.Library, 0, len(libraries))
	for _, library := range libraries {
		if libraryID != 0 && library.ID != libraryID {
			continue
		}
		selected = append(selected, library)
	}
	if len(selected) == 0 {
		return scanner.Result{}, sql.ErrNoRows
	}
	if !a.beginScan(len(selected)) {
		return scanner.Result{}, errScanBusy
	}
	// 用 defer 释放扫描状态与 NFO 写入通道：中途 panic 时若走不到 endScan，
	// nfoOwner 会永久停留在 "scan"，此后探测/刮削全部被拒（需要重启才能恢复）。
	result := scanner.Result{}
	var scanErr error
	defer func() { a.endScan(scanErr) }()
	for index, library := range selected {
		a.setScanLibrary(index+1, library)
		slog.Info("正在扫描媒体库", "library_id", library.ID, "name", library.Name, "path", library.Path)
		current, err := scanner.ScanWithProgress(a.db, library, a.updateScanProgress)
		if err != nil {
			slog.Error("媒体库扫描失败", "library", library.Name, "error", err)
			scanErr = err
			break
		}
		slog.Info("媒体库扫描完成", "library", library.Name,
			"success", current.Success, "pending", current.Pending,
			"incompatible", current.Incompatible, "failed", current.Failed)
		result.Success += current.Success
		result.Pending += current.Pending
		result.Incompatible += current.Incompatible
		result.Failed += current.Failed
	}
	// endScan 由上面的 defer 负责调用（panic 时也必须释放）。
	a.cache.Clear()
	return result, scanErr
}

// scanning 返回当前是否已有扫描在跑（仅用于提前拒绝，真正的互斥由 beginScan 保证）。
func (a *App) scanning() bool {
	a.scanMu.RLock()
	defer a.scanMu.RUnlock()
	return a.scanStatus.Running
}

func (a *App) adminScan(c *gin.Context) {
	if a.scanning() {
		c.JSON(http.StatusConflict, gin.H{"error": errScanBusy.Error()})
		return
	}
	libraryID, _ := strconv.ParseInt(c.Query("library_id"), 10, 64)
	taskID := a.startTask("scan")
	result, err := a.scanLibraries(libraryID)
	a.finishTask(taskID, err)
	if err != nil {
		if errors.Is(err, errScanBusy) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if store.NotFound(err) {
			c.JSON(404, gin.H{"error": "library not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, result)
}

// adminReindex 全库重建索引（与扫描同一路径，仅范围是全部媒体库）。
func (a *App) adminReindex(c *gin.Context) {
	if a.scanning() {
		c.JSON(http.StatusConflict, gin.H{"error": errScanBusy.Error()})
		return
	}
	slog.Info("开始重建索引")
	taskID := a.startTask("reindex")
	result, err := a.scanLibraries(0)
	a.finishTask(taskID, err)
	if err != nil {
		if errors.Is(err, errScanBusy) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if store.NotFound(err) {
			c.JSON(200, result)
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	slog.Info("重建索引完成", "success", result.Success, "pending", result.Pending,
		"incompatible", result.Incompatible, "failed", result.Failed)
	c.JSON(200, result)
}

func (a *App) adminItems(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	// library_id 为 0/缺省表示「全部媒体库」——媒体墙的库筛选 Tab 走这里。
	libraryID, _ := strconv.ParseInt(c.Query("library_id"), 10, 64)
	// 按刮削结果筛选时默认不限状态：刮削失败/待确认的条目多数仍是 pending，
	// 若沿用「只显示可播放」的默认口径，用户会看到筛选结果是空的。
	status := c.Query("status")
	if c.Query("scrape") != "" && status == "" {
		status = store.StatusAll
	}
	// genre/tag/studio/person/collection 来自详情抽屉的实体跳转（点演员/类型/厂商/合集）。
	ms, total, err := a.db.SearchAdmin(store.AdminQuery{
		LibraryID:  libraryID,
		Term:       c.Query("search"),
		Status:     status,
		Protocol:   strings.ToLower(c.Query("source_protocol")),
		Genre:      c.Query("genre"),
		Tag:        c.Query("tag"),
		Studio:     c.Query("studio"),
		Person:     c.Query("person"),
		Collection: c.Query("collection"),
		Scrape:     c.Query("scrape"),
		SortBy:     c.DefaultQuery("sort", "title"),
		Desc:       strings.EqualFold(c.Query("order"), "desc"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 媒体墙需要观看状态（已看/收藏/进度）做角标与进度条。
	ids := make([]int64, 0, len(ms))
	for _, movie := range ms {
		ids = append(ids, movie.ID)
	}
	dataMap, _ := a.db.DataFor(ids)
	userData := make(map[string]gin.H, len(dataMap))
	for id, data := range dataMap {
		userData[strconv.FormatInt(id, 10)] = gin.H{
			"played": data.Played, "favorite": data.IsFavorite,
			"position_ticks": data.PositionTicks, "play_count": data.PlayCount,
		}
	}
	c.JSON(200, gin.H{"items": ms, "total": total, "limit": limit, "offset": offset, "userdata": userData})
}

func (a *App) adminDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if err = a.db.DeleteMovie(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	_ = a.db.BumpVersion(movie.LibraryID)
	a.cache.Clear()
	c.Status(http.StatusNoContent)
}

func (a *App) adminManual(c *gin.Context) {
	var req struct {
		LibraryID     int64    `json:"library_id"`
		SourcePath    string   `json:"source_path"`
		SourceURL     string   `json:"source_url"`
		Title         string   `json:"title"`
		Year          int      `json:"year"`
		Plot          string   `json:"plot"`
		Number        string   `json:"number"`
		OriginalTitle string   `json:"original_title"`
		Director      string   `json:"director"`
		Series        string   `json:"series"`
		Maker         string   `json:"maker"`
		Label         string   `json:"label"`
		PosterPath    string   `json:"poster_path"`
		BackdropPath  string   `json:"backdrop_path"`
		Genres        []string `json:"genres"`
		Tags          []string `json:"tags"`
		Studios       []string `json:"studios"`
	}
	if c.ShouldBindJSON(&req) != nil || req.SourcePath == "" || req.Title == "" || !scanner.ValidHTTP(req.SourceURL) {
		c.JSON(400, gin.H{"error": "source_path、title 和 http(s) source_url required"})
		return
	}
	if req.LibraryID == 0 {
		libs, _ := a.db.Libraries()
		if len(libs) > 0 {
			req.LibraryID = libs[0].ID
		}
	}
	if req.LibraryID == 0 {
		c.JSON(400, gin.H{"error": "library required"})
		return
	}
	if err := os.WriteFile(req.SourcePath, []byte(req.SourceURL+"\n"), 0644); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	nfoPath := strings.TrimSuffix(req.SourcePath, filepath.Ext(req.SourcePath)) + ".nfo"
	meta := nfo.FromFields(req.Title, req.Year)
	meta.Number = req.Number
	meta.OriginalTitle = req.OriginalTitle
	meta.Plot = req.Plot
	meta.Director = req.Director
	meta.Series = req.Series
	meta.Maker = req.Maker
	meta.Label = req.Label
	meta.Genres = req.Genres
	meta.Tags = req.Tags
	meta.Studios = req.Studios
	if err := nfo.SaveAtomic(nfoPath, meta); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	size, mtime := scanner.SourceStat(req.SourcePath)
	u, _ := url.Parse(req.SourceURL)
	m := store.Movie{LibraryID: req.LibraryID, SourcePath: req.SourcePath, SourceProtocol: strings.ToLower(u.Scheme), SourceContainer: strings.TrimPrefix(strings.ToLower(filepath.Ext(u.Path)), "."), Status: "manual", NFOPath: nfoPath, OutputDir: filepath.Dir(req.SourcePath), Number: req.Number, Title: req.Title, OriginalTitle: req.OriginalTitle, Year: req.Year, Plot: req.Plot, Director: req.Director, Series: req.Series, Maker: req.Maker, Label: req.Label, Genres: req.Genres, Tags: req.Tags, Studios: req.Studios, PosterPath: req.PosterPath, BackdropPath: req.BackdropPath}
	id, err := a.db.UpsertMovie(m, size, mtime)
	if err == nil {
		err = a.db.BumpVersion(req.LibraryID)
	}
	a.cache.Clear()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	slog.Info("手动补录入库", "movie_id", id, "title", req.Title, "path", req.SourcePath)
	c.JSON(200, gin.H{"id": id, "status": "success"})
}

func (a *App) adminEdit(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	m, e := a.db.Movie(id)
	if e != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	var req struct {
		Title *string `json:"title"`
		Plot  *string `json:"plot"`
		Year  *int    `json:"year"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, gin.H{"error": "invalid json"})
		return
	}
	if _, e = nfo.Read(m.NFOPath); e != nil {
		c.JSON(400, gin.H{"error": "nfo not found"})
		return
	}
	// 走标签级更新而不是「读成结构体 → 整体重写」：NFO 里可能有外部工具写的
	// 扩展标签，整体重写会静默丢掉它们（见 internal/nfo/update.go 的说明）。
	// 空值表示「本次不修改该字段」，不删除已有内容。
	fields := nfo.ScrapeFields{Overwrite: true}
	if req.Title != nil {
		fields.Title = strings.TrimSpace(*req.Title)
	}
	if req.Plot != nil {
		fields.Plot = *req.Plot
	}
	if req.Year != nil {
		fields.Year = *req.Year
	}
	if fields.Title == "" && fields.Plot == "" && fields.Year == 0 {
		c.JSON(400, gin.H{"error": "没有可写入的字段"})
		return
	}
	if e = nfo.UpdateScraped(m.NFOPath, fields); e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	if fields.Title != "" {
		m.Title = fields.Title
	}
	if fields.Plot != "" {
		m.Plot = fields.Plot
	}
	if fields.Year > 0 {
		m.Year = fields.Year
	}
	size, mtime := scanner.SourceStat(m.SourcePath)
	_, e = a.db.UpsertMovie(m, size, mtime)
	if e == nil {
		e = a.db.BumpVersion(m.LibraryID)
	}
	a.cache.Clear()
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "success"})
}

func (a *App) adminReread(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	libraries, err := a.db.Libraries()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	var library store.Library
	for _, candidate := range libraries {
		if candidate.ID == movie.LibraryID {
			library = candidate
			break
		}
	}
	if library.ID == 0 {
		c.JSON(404, gin.H{"error": "library not found"})
		return
	}
	// 单文件重扫：整库重扫在「点一条重读源」这种场景下代价过高，
	// 且会顺带触发 DeleteMissingSources，风险与收益不成比例。
	if _, err = scanner.RescanOne(a.db, library, movie.SourcePath); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	movie, err = a.db.Movie(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "source no longer indexed"})
		return
	}
	a.cache.Clear()
	c.JSON(200, gin.H{"status": movie.Status, "protocol": movie.SourceProtocol})
}

func saveUploadedWebP(header *multipart.FileHeader, destination string) error {
	file, err := header.Open()
	if err != nil {
		return err
	}
	defer file.Close()
	return imageutil.EncodeWebP(file, destination+".tmp")
}

func (a *App) adminImage(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	m, e := a.db.Movie(id)
	if e != nil {
		c.Status(404)
		return
	}
	f, e := c.FormFile("file")
	if e != nil {
		c.JSON(400, gin.H{"error": "file required"})
		return
	}
	kind := strings.ToLower(c.Param("kind"))
	names := map[string]string{"poster": "poster.webp", "primary": "poster.webp", "backdrop": "fanart.webp", "fanart": "fanart.webp", "landscape": "landscape.webp"}
	name, ok := names[kind]
	if !ok {
		c.JSON(400, gin.H{"error": "unsupported image kind"})
		return
	}
	dest := filepath.Join(m.OutputDir, name)
	if e = saveUploadedWebP(f, dest); e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	if e = os.Rename(dest+".tmp", dest); e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	switch kind {
	case "poster", "primary":
		m.PosterPath = dest
	case "backdrop", "fanart":
		m.BackdropPath = dest
	case "landscape":
		m.LandscapePath = dest
	}
	// 同名图片被覆盖，立即失效它的 tag 缓存，否则客户端几分钟内仍拿旧图。
	a.invalidateImageTag(dest)
	size, mtime := scanner.SourceStat(m.SourcePath)
	_, e = a.db.UpsertMovie(m, size, mtime)
	if e == nil {
		e = a.db.BumpVersion(m.LibraryID)
	}
	a.cache.Clear()
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "success", "path": dest})
}

func (a *App) adminSettings(c *gin.Context) {
	c.JSON(200, gin.H{
		"listen":       a.cfg.Addr(),
		"db_path":      a.cfg.DBPath,
		"debug":        a.cfg.Debug,
		"cache":        "redis",
		"redis_addr":   a.cfg.RedisAddr,
		"redis_db":     a.cfg.RedisDB,
		"redis_online": true,
	})
}

func (a *App) adminTasks(c *gin.Context) {
	a.taskMu.Lock()
	items := append([]task(nil), a.tasks...)
	a.taskMu.Unlock()
	running := false
	for _, item := range items {
		if item.Status == "running" {
			running = true
			break
		}
	}
	c.JSON(200, gin.H{"items": items, "running": running})
}

func (a *App) adminStatus(c *gin.Context) {
	result := gin.H{}
	for _, status := range []string{"success", "manual", "pending", "incompatible"} {
		count, err := a.db.CountByStatus(status)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		result[status] = count
	}
	c.JSON(200, result)
}

func (a *App) adminProbe(c *gin.Context) {
	v, e := a.db.Probes(100)
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	c.JSON(200, gin.H{"items": v})
}

func (a *App) adminClearProbes(c *gin.Context) {
	if err := a.db.ClearProbes(); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// —— API 密钥管理：密钥可作 X-Emby-Token / api_key 直接调用 Emby 接口 ——

func (a *App) adminAPIKeys(c *gin.Context) {
	keys, err := a.db.APIKeys()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": keys, "total": len(keys)})
}

func (a *App) adminCreateAPIKey(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(400, gin.H{"error": "name required"})
		return
	}
	key, err := a.db.CreateAPIKey(req.Name)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	slog.Info("创建 API 密钥", "name", key.Name)
	c.JSON(200, key)
}

func (a *App) adminDeleteAPIKey(c *gin.Context) {
	key := c.Param("key")
	if err := a.db.DeleteAPIKey(key); err != nil {
		if store.NotFound(err) {
			c.JSON(404, gin.H{"error": "key not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.cache.Delete("apikey:" + key)
	prefix := key
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	slog.Info("删除 API 密钥", "key_prefix", prefix)
	c.Status(http.StatusNoContent)
}
