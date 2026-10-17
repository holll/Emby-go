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
	counts, e := a.db.LibraryCounts()
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	// 保留 Library 原本的 Id/Name/Path 键名并额外带 count，
	// 前端既有代码（lib.Id / lib.Name）不受影响。
	items := make([]gin.H, 0, len(v))
	for _, lib := range v {
		// 封面 URL 由后端拼好：库封面的图片端点按「外部库 id」取图，让前端自己拼
		// 容易踩内/外部 id 的差别。空串表示该库当前没有可用封面。
		cover := ""
		if a.libraryCoverPath(lib) != "" {
			cover = "/Items/" + externalLibraryID(lib.ID) + "/Images/Primary"
		}
		items = append(items, gin.H{
			"Id":    lib.ID,
			"Name":  lib.Name,
			"Path":  lib.Path,
			"count": counts[lib.ID],
			"cover": cover,
		})
	}
	c.JSON(200, gin.H{"items": items, "total": len(items)})
}

// adminDeleteLibrary 删除媒体库及其影片索引（不删磁盘文件），随后清缓存。
func (a *App) adminDeleteLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if err = a.db.DeleteIndexedLibrary(id); err != nil {
		if store.NotFound(err) {
			c.JSON(404, gin.H{"error": "library not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.cache.Clear()
	slog.Info("删除媒体库", "library_id", id)
	c.Status(http.StatusNoContent)
}

// adminMoveLibrary 调整媒体库显示顺序。这个顺序同时决定管理端列表与
// Emby 客户端 /Users/{id}/Views 的库顺序，客户端下次刷新即可见。
func (a *App) adminMoveLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	var req struct {
		Direction string `json:"direction"`
	}
	if c.ShouldBindJSON(&req) != nil || (req.Direction != "up" && req.Direction != "down") {
		c.JSON(400, gin.H{"error": "direction must be up or down"})
		return
	}
	if err = a.db.MoveLibrary(id, req.Direction == "up"); err != nil {
		if store.NotFound(err) {
			c.JSON(404, gin.H{"error": "library not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 顺序变了，库列表与 Views 的缓存都要失效。
	a.cache.Clear()
	c.JSON(200, gin.H{"status": "success"})
}

// adminLibraryCover 上传媒体库封面：统一转 webp 落到库根目录的固定文件名，
// 优先级高于目录里自带的 poster/cover，也高于借用影片代表图。
func (a *App) adminLibraryCover(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	lib, err := a.db.Library(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "library not found"})
		return
	}
	f, err := c.FormFile("file")
	if err != nil {
		c.JSON(400, gin.H{"error": "file required"})
		return
	}
	dest := filepath.Join(lib.Path, libraryCoverFileName)
	if err = saveUploadedWebP(f, dest); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err = os.Rename(dest+".tmp", dest); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.invalidateImageTag(dest) // 同名文件被覆盖，立刻失效它的 tag 缓存
	a.cache.Clear()            // libraryCoverPath 的结果按 g:version 缓存，必须清
	slog.Info("上传媒体库封面", "library_id", id, "path", dest)
	c.JSON(200, gin.H{"status": "success", "path": dest})
}

// adminLibraryCoverDelete 移除上传的封面，回退到目录自带图或影片代表图。
func (a *App) adminLibraryCoverDelete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	lib, err := a.db.Library(id)
	if err != nil {
		c.JSON(404, gin.H{"error": "library not found"})
		return
	}
	dest := filepath.Join(lib.Path, libraryCoverFileName)
	if err = os.Remove(dest); err != nil && !os.IsNotExist(err) {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.invalidateImageTag(dest)
	a.cache.Clear()
	slog.Info("移除媒体库封面", "library_id", id)
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
	a.scanStatus.Skipped, a.scanStatus.Failed = p.Result.Skipped, p.Result.Failed
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
			"skipped", current.Skipped, "failed", current.Failed)
		result.Success += current.Success
		result.Pending += current.Pending
		result.Skipped += current.Skipped
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
		"skipped", result.Skipped, "failed", result.Failed)
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
	// 状态筛选直接透传：status=needs_attention 由 store 翻译成「pending 且刮削出错」，
	// 与 scrape=failed 时代不同，这里不再需要先把 status 提升为「不限状态」。
	// genre/tag/studio/person/collection 来自详情抽屉的实体跳转（点演员/类型/厂商/合集）。
	ms, total, err := a.db.SearchAdmin(store.AdminQuery{
		LibraryID:  libraryID,
		Term:       c.Query("search"),
		Status:     c.Query("status"),
		Protocol:   strings.ToLower(c.Query("source_protocol")),
		Genre:      c.Query("genre"),
		Tag:        c.Query("tag"),
		Studio:     c.Query("studio"),
		Person:     c.Query("person"),
		Collection: c.Query("collection"),
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
	dataMap, ok := a.itemDataFor(c, ids)
	if !ok {
		return
	}
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
	if err = a.db.DeleteIndexedMovie(id); err != nil {
		if store.NotFound(err) {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.cache.Clear()
	c.Status(http.StatusNoContent)
}

// editRequest 元数据编辑请求体。指针字段为空表示「本次不修改该字段」。
//
// 只保留可编辑的元数据项：源地址（.strm 路径与媒体直链）不在这里——
// 换源继续走「重读源」链路，避免两条改源路径互相打架。
type editRequest struct {
	Title         *string   `json:"title"`
	Number        *string   `json:"number"`
	OriginalTitle *string   `json:"original_title"`
	Plot          *string   `json:"plot"`
	Year          *int      `json:"year"`
	Genres        *[]string `json:"genres"`
	Tags          *[]string `json:"tags"`
	Studios       *[]string `json:"studios"`
	Maker         *string   `json:"maker"`
	Label         *string   `json:"label"`
	LockedFields  *[]string `json:"locked_fields"`
	LockData      *bool     `json:"lock_data"`
}

// adminEdit 编辑影片元数据。
//
// 只写 NFO，不写 movies 表：元数据真源是 NFO，索引由 `RescanOne` 重建——
// 直接 UpsertMovie 会把状态写回 success，人工录入的分类就留不住了
// （重扫时 scanner 会按库里已有的 manual 集合保留它）。
func (a *App) adminEdit(c *gin.Context) { a.adminEditMode(c, false) }

func (a *App) adminPatch(c *gin.Context) { a.adminEditMode(c, true) }

func (a *App) adminEditMode(c *gin.Context, strict bool) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	var req editRequest
	if strict {
		req, e = decodeEditPatch(c.Request.Body)
	} else {
		e = c.ShouldBindJSON(&req)
	}
	if e != nil {
		message := "invalid json"
		if strict {
			message = "invalid metadata fields"
		}
		c.JSON(400, gin.H{"error": message})
		return
	}
	if req.LockedFields != nil {
		for _, field := range *req.LockedFields {
			if strings.ContainsAny(field, "|\r\n") {
				c.JSON(400, gin.H{"error": "字段锁名称不能包含分隔符或换行"})
				return
			}
		}
	}
	// 覆盖 NFO、manual 标记和索引刷新必须处于同一通道内，阻止旧扫描结果回写。
	if !a.claimNFO("edit") {
		c.JSON(http.StatusConflict, gin.H{"error": a.nfoBusyOwner() + "正在进行中，请稍后再试"})
		return
	}
	defer a.releaseNFO("edit")
	m, e := a.db.Movie(id)
	if e != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	nfoPath := editNFOPath(m)
	if _, e = nfo.Read(nfoPath); e != nil && !errors.Is(e, os.ErrNotExist) {
		c.JSON(400, gin.H{"error": "nfo 不可读: " + e.Error()})
		return
	}
	// 走标签级更新而不是「读成结构体 → 整体重写」：NFO 里可能有外部工具写的
	// 扩展标签，整体重写会静默丢掉它们（见 internal/nfo/update.go 的说明）。
	// Overwrite=true：人工显式覆盖。旧PUT空值不修改；新PATCH独立传清空标签。
	fields := nfo.ScrapeFields{Overwrite: true, ManualEdit: true, LockedFields: req.LockedFields, LockData: req.LockData}
	if req.Title != nil {
		fields.Title = strings.TrimSpace(*req.Title)
	}
	if req.Number != nil {
		fields.Number = strings.TrimSpace(*req.Number)
	}
	if req.OriginalTitle != nil {
		fields.OriginalTitle = strings.TrimSpace(*req.OriginalTitle)
	}
	if req.Plot != nil {
		fields.Plot = strings.TrimSpace(*req.Plot)
	}
	if req.Year != nil {
		fields.Year = *req.Year
	}
	if req.Genres != nil {
		fields.Genres = trimList(*req.Genres)
	}
	if req.Tags != nil {
		fields.Tags = trimList(*req.Tags)
	}
	if req.Studios != nil {
		fields.Studios = trimList(*req.Studios)
	}
	if req.Maker != nil {
		fields.Maker = strings.TrimSpace(*req.Maker)
	}
	if req.Label != nil {
		fields.Label = strings.TrimSpace(*req.Label)
	}
	if strict {
		fields.ClearFields = req.clearTags()
	}
	if e = nfo.UpdateScraped(nfoPath, fields); e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	// 文件已经落盘；后续任何失败必须表达为部分成功，不能伪报保存失败或全成功。
	a.finishEditedNFO(c, m, nfoPath, "")
}

// trimList 去掉列表项首尾空白并丢弃空项；返回非 nil 空切片表示「清空该列表」。
func trimList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (a *App) adminReread(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	if !a.claimNFO("edit") {
		c.JSON(http.StatusConflict, gin.H{"error": a.nfoBusyOwner() + "正在进行中，请稍后再试"})
		return
	}
	defer a.releaseNFO("edit")
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
	// 同名图片被覆盖，立即失效它的 tag 缓存，否则客户端几分钟内仍拿旧图。
	a.invalidateImageTag(dest)
	// Do not write the pre-upload Movie snapshot back over concurrent edits.
	// This transaction covers the indexed path and versions, not the saved file.
	e = a.db.SetIndexedImage(id, kind, dest)
	a.cache.Clear()
	if e != nil {
		c.JSON(500, gin.H{"error": "image saved but index update failed", "code": "IMAGE_INDEX_FAILED", "saved": true, "indexed": false})
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
	for _, status := range []string{"success", "manual", "pending"} {
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
	if err := a.clearDiagnosticProbes(); err != nil {
		c.JSON(500, gin.H{"error": "probe history clear failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

// —— API 密钥管理：密钥可作 X-Emby-Token / api_key 直接调用 Emby 接口 ——

// API 密钥是管理员凭据（D02，2026-10-17 已确认）：当前单用户版本中
// 任何密钥都可调用全部管理与 Emby 接口；未来引入多用户后，非管理员用户
// 不得创建或管理 API 密钥。列表当前仍返回密钥原文，属 BE-03/D03 的
// 摘要与 key_id 迁移边界，迁移前不要据此宣称「列表最小披露」。
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
	slog.Info("创建 API 密钥")
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
	// Even a prefix is credential material; names may also contain pasted secrets.
	slog.Info("删除 API 密钥")
	c.Status(http.StatusNoContent)
}
