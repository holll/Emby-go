package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/config"
	"emby-go/internal/imagecache"
	"emby-go/internal/imageutil"
	"emby-go/internal/nfo"
	"emby-go/internal/outbound"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

// movieImagePath 返回影片某 Emby 图片类型对应的磁盘文件路径。
// Thumb（宽图）在缺 landscape 时回退主海报，保证给客户端的 Thumb 标记始终可取图。
func movieImagePath(m store.Movie, kind string) string {
	switch strings.ToLower(kind) {
	case "primary", "poster":
		return m.PosterPath
	case "thumb":
		if m.LandscapePath != "" {
			return m.LandscapePath
		}
		return m.PosterPath
	case "backdrop", "fanart":
		return m.BackdropPath
	case "landscape":
		return m.LandscapePath
	}
	return ""
}

func (a *App) image(c *gin.Context) {
	rawID := c.Param("id")
	kind := c.Param("kind")
	if id, err := strconv.ParseInt(rawID, 10, 64); err == nil {
		// 媒体库外部 id：库封面（库根目录自带图优先，否则借库内影片代表图）。
		if libID, ok := parseLibraryExternal(id); ok {
			if poster := a.libraryCoverPathByID(libID); poster != "" {
				a.serveImage(c, poster)
				return
			}
			c.Status(404)
			return
		}
		// 数值 id 优先命中影片；影片不存在/不可见时回退为该媒体库的封面。
		if m, ok := a.cachedMovie(id); ok && m.IsVisible() {
			if p := movieImagePath(m, kind); p != "" {
				a.serveImage(c, p)
				return
			}
			c.Status(404)
			return
		}
		if poster := a.libraryCoverPathByID(id); poster != "" && strings.EqualFold(kind, "Primary") {
			a.serveImage(c, poster)
			return
		}
		c.Status(404)
		return
	}
	// 合集：boxsets 媒体库文件夹用任一合集海报；boxset:<b64> 用该合集海报。
	if p := a.boxsetPoster(rawID); p != "" {
		a.serveImage(c, p)
		return
	}
	// 虚拟实体封面：任何 ImageType 都回代表性海报，保证 Tag/Genre 网格有图。
	if kind, name, ok := entityKind(rawID); ok {
		// 演员优先回本地头像副本；没有头像时保留原占位行为（参演影片海报）。
		if kind == "Person" {
			if path, _ := a.personAvatar(name); path != "" {
				a.serveImage(c, path)
				return
			}
		}
		if poster := a.entityPosterPath(kind, name); poster != "" {
			a.serveImage(c, poster)
			return
		}
	}
	c.Status(404)
}

const (
	// imageMaxAge 图片响应的客户端/边缘缓存时长。图片只在刮削/上传后变化，
	// 变化时 ETag（文件 mtime）会变；取 24 小时让 Cloudflare 边缘与浏览器
	// 长时间命中，重刮后的新图最迟一天生效（需要立刻生效时刷新 URL）。
	imageMaxAge = 24 * time.Hour
	// imageMetaTTL 图片元信息（影片图片路径、演员头像路径）的进程内缓存时长。
	imageMetaTTL = time.Minute
	// imageThumbTTL 缩略图字节的进程内缓存时长。
	imageThumbTTL = 30 * time.Minute
	// imageMetaCacheSize / imageThumbCacheSize 两个 LRU 的条目上限。
	imageMetaCacheSize  = 8192
	imageThumbCacheSize = 512
	// maxThumbConcurrency 同时生成缩略图的数量上限（解码 + 缩放 + 编码都是 CPU 活）。
	maxThumbConcurrency = 4
	// maxThumbEdge 目标边长上限：超过就不生成，直接发原图。
	maxThumbEdge = 4000
	// thumbDiskKeyVersion 是磁盘变体的命名空间：编码参数或缩放实现变化时递增，
	// 旧变体自然过期淘汰，不需要逐条改文件名。
	thumbDiskKeyVersion = "v1"
)

// imageEdgeBuckets 是允许生成的边长档位（像素）。
//
// 客户端可以请求任意 maxWidth/maxHeight；若每个数值都独立生成变体，磁盘上
// 会有几百种彼此几乎无法复用的尺寸。按档位**向下**取整把变体数量收在一个
// 可枚举的集合里，同时保证响应不超过请求上限——客户端不会拿到比 maxWidth
// 更大的图（那会破坏布局语义），代价是可能略小一档。
var imageEdgeBuckets = []int{
	32, 40, 50, 60, 72, 80, 96, 100, 120, 144, 160, 180, 200,
	240, 300, 360, 400, 480, 540, 720, 1080, 1440, 2160, 4000,
}

// normalizeImageEdge 把请求边长向下对齐到最近的档位。
// 小于最小档位时保留原值：再小的图本来就很便宜，抬到 32 反而违反 maxWidth 上限。
func normalizeImageEdge(edge int) int {
	if edge <= 0 {
		return 0
	}
	if edge < imageEdgeBuckets[0] {
		return edge
	}
	for _, imageEdgeBucket := range slices.Backward(imageEdgeBuckets) {
		if edge >= imageEdgeBucket {
			return imageEdgeBucket
		}
	}
	return imageEdgeBuckets[0]
}

// firstCollectionPoster 返回第一个有海报的合集封面（没有则空串）。
func (a *App) firstCollectionPoster(names []string) string {
	for _, name := range names {
		if poster, _ := a.db.CollectionPoster(name); poster != "" {
			return poster
		}
	}
	return ""
}

// cachedMovie 取影片（进程内短缓存）。
// 图片接口每张图都要拿一次图片路径，直接查库会让整页海报串行排队
// （SQLite 写只有一条连接，读虽已放开并发，但每张图一次查询依然是纯浪费）。
func (a *App) cachedMovie(id int64) (store.Movie, bool) {
	// Capture before reading: a late old read may only fill its old namespace.
	version := a.db.Version("g:version")
	key := "m:" + version + ":" + strconv.FormatInt(id, 10)
	if raw, ok := a.imgMeta.Get(key); ok {
		var m store.Movie
		if json.Unmarshal(raw, &m) == nil {
			return m, true
		}
	}
	m, err := a.db.Movie(id)
	if err != nil {
		return store.Movie{}, false
	}
	if raw, err := json.Marshal(m); err == nil {
		a.imgMeta.Set(key, raw, imageMetaTTL)
	}
	return m, true
}

// serveImage 统一的图片响应：带 ETag/Cache-Control，按需缩放，未请求缩放则原图直出。
// 命中 If-None-Match 时直接 304——连文件都不打开。
func (a *App) serveImage(c *gin.Context, path string) {
	tag := a.posterTag(path)
	c.Header("ETag", `"`+tag+`"`)
	c.Header("Cache-Control", "public, max-age="+strconv.Itoa(int(imageMaxAge.Seconds()))+", must-revalidate")
	if ifNoneMatchHit(c.GetHeader("If-None-Match"), tag) {
		c.Status(http.StatusNotModified)
		return
	}
	if data, ok := a.thumbnail(c, path, tag); ok {
		c.Data(http.StatusOK, "image/webp", data)
		return
	}
	c.File(path)
}

// thumbFlight 是一次「同 key 生成中」的登记：leader 负责生成，follower 等结果。
type thumbFlight struct {
	done chan struct{}
	data []byte
	ok   bool
}

// thumbCacheKey 是变体的唯一标识：路径、源图 tag（mtime+size）、目标尺寸、画质。
// 任何一项变化都必须产生不同的 key，否则会拿到旧图、错尺寸或错画质。
func thumbCacheKey(path, tag string, width, height, quality int) string {
	return thumbDiskKeyVersion + "|t:" + path + ":" + tag + ":" +
		strconv.Itoa(width) + "x" + strconv.Itoa(height) + ":q" + strconv.Itoa(quality)
}

// thumbnail 返回按请求参数生成的缩略图；未请求缩放或生成失败时返回 false（调用方发原图）。
//
// 命中顺序：进程内 LRU → 磁盘变体 → 同 key 合并生成。前两层都是纯读，
// 只有都落空才值得花 CPU；磁盘层让重启进程、或内存 LRU 被新条目顶掉之后
// 仍不必重新解码。
func (a *App) thumbnail(c *gin.Context, path, tag string) ([]byte, bool) {
	width, height, quality := imageResizeParams(c)
	if width <= 0 && height <= 0 {
		return nil, false
	}
	key := thumbCacheKey(path, tag, width, height, quality)
	if data, ok := a.imgThumb.Get(key); ok {
		return data, true
	}
	if data, ok := a.imgDisk.Get(key); ok {
		a.imgThumb.Set(key, data, imageThumbTTL)
		return data, true
	}
	// 同一张图的同一个尺寸常被并发请求（<img> 与 srcset 同时开）。
	// 信号量只能限流：后到者会排队、复查命中，但排在前面的一批仍会各自算一遍。
	flight, leader := a.beginThumbFlight(key)
	if !leader {
		select {
		case <-flight.done:
			if flight.ok {
				return flight.data, true
			}
			return nil, false
		case <-c.Request.Context().Done():
			return nil, false
		}
	}
	// leader 生成期间一旦 panic（解码器崩溃、编码器 bug），finishThumbFlight 就
	// 不会执行：该 key 的 flight 永久残留、done 永不 close，之后所有同 key 请求
	// 都阻塞在 <-flight.done 直到客户端 ctx 超时，这张图在进程生命周期内彻底
	// 不可用。defer 保证注销一定发生（follower 拿 ok=false 走回退原图），
	// 再把 panic 原样抛出，保留 monitor/日志对异常的记录。
	data, ok := func() (data []byte, ok bool) {
		defer func() {
			if r := recover(); r != nil {
				a.finishThumbFlight(key, flight, nil, false)
				panic(r)
			}
		}()
		return a.generateThumbnail(c, key, path, width, height, quality)
	}()
	a.finishThumbFlight(key, flight, data, ok)
	return data, ok
}

// generateThumbnail 是 leader 的生成路径：限流 → 复查 → 生成 → 落盘。
func (a *App) generateThumbnail(c *gin.Context, key, path string, width, height, quality int) ([]byte, bool) {
	// 限流：解码/缩放/编码是纯 CPU 活，一次性放开会把 CPU 打满。
	select {
	case a.thumbSem <- struct{}{}:
		defer func() { <-a.thumbSem }()
	case <-c.Request.Context().Done():
		return nil, false
	}
	// 排队期间可能已被别的请求填上（内存或磁盘）。
	if data, ok := a.imgThumb.Get(key); ok {
		return data, true
	}
	if data, ok := a.imgDisk.Get(key); ok {
		a.imgThumb.Set(key, data, imageThumbTTL)
		return data, true
	}
	generate := a.thumbGenerate
	if generate == nil {
		// 测试里用字面量构造的 App 没有走 newApp；给个默认实现免得 panic。
		generate = imageutil.Thumbnail
	}
	data, err := generate(path, width, height, quality)
	if err != nil || len(data) == 0 {
		// 生成失败（格式不支持、文件坏）不算错误：回退原图，用户仍能看到图。
		if err != nil {
			slog.Debug("生成缩略图失败，回退原图", "path", path, "error", err)
		}
		return nil, false
	}
	a.imgThumb.Set(key, data, imageThumbTTL)
	if err := a.imgDisk.Put(key, data); err != nil {
		// 写盘失败只影响下次复用，这一次的响应照常返回。
		slog.Debug("写入缩略图磁盘缓存失败", "path", path, "error", err)
	}
	return data, true
}

// beginThumbFlight 登记一次生成；leader=true 表示由当前请求负责生成，
// false 表示已有同 key 的生成在进行，等待它的结果即可。
func (a *App) beginThumbFlight(key string) (*thumbFlight, bool) {
	a.thumbMu.Lock()
	defer a.thumbMu.Unlock()
	if a.thumbFlights == nil {
		// 字面量构造的 App（测试）没有初始化这张表。
		a.thumbFlights = make(map[string]*thumbFlight)
	}
	if flight, ok := a.thumbFlights[key]; ok {
		return flight, false
	}
	flight := &thumbFlight{done: make(chan struct{})}
	a.thumbFlights[key] = flight
	return flight, true
}

// finishThumbFlight 发布结果并注销登记。先写字段再 close(done)：
// follower 收到关闭信号时一定能读到完整结果。
func (a *App) finishThumbFlight(key string, flight *thumbFlight, data []byte, ok bool) {
	a.thumbMu.Lock()
	flight.data, flight.ok = data, ok
	delete(a.thumbFlights, key)
	a.thumbMu.Unlock()
	close(flight.done)
}

// imageResizeParams 解析 Emby 客户端的缩放参数。
// 只认尺寸类参数：只给 quality 时无法判断目标尺寸，按原图处理。
func imageResizeParams(c *gin.Context) (width, height, quality int) {
	width = queryInt(c, "maxWidth", "width")
	height = queryInt(c, "maxHeight", "height")
	quality = queryInt(c, "quality")
	if width > maxThumbEdge {
		width = maxThumbEdge
	}
	if height > maxThumbEdge {
		height = maxThumbEdge
	}
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	// 对齐到档位（向下）：控制变体总量，且保证响应不超过请求上限。
	width = normalizeImageEdge(width)
	height = normalizeImageEdge(height)
	if quality < 1 || quality > 100 {
		quality = 0 // 0 表示用内置默认画质
	}
	return width, height, quality
}

// openImageDisk 打开缩略图变体目录；目录建不出来就降级为纯内存缓存——
// 缓存不可用不该让图片接口（乃至整个服务）起不来。
func openImageDisk(cfg config.Config) *imagecache.Disk {
	cacheCfg := cfg.ImageCache.ApplyDefaults()
	dir := strings.TrimSpace(cacheCfg.Dir)
	if dir == "" {
		// 默认放在库文件旁边：部署时一个目录就是一个实例的运行时数据。
		dir = filepath.Join(filepath.Dir(cfg.DBPath), "image-cache")
	}
	disk, err := imagecache.Open(dir, cacheCfg.MaxBytes, cacheCfg.TTL(), cacheCfg.IsEnabled())
	if err != nil {
		slog.Warn("缩略图磁盘缓存不可用，退回内存缓存", "dir", dir, "error", err)
		return imagecache.Disabled()
	}
	if disk.Enabled() {
		slog.Info("缩略图磁盘缓存已启用", "dir", disk.Dir(), "max_bytes", cacheCfg.MaxBytes, "ttl_hours", cacheCfg.TTLHours)
	}
	return disk
}

// queryInt 取第一个能解析为正整数的查询参数，都没有则返回 0。
func queryInt(c *gin.Context, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(c.Query(key))
		if value == "" {
			continue
		}
		if number, err := strconv.Atoi(value); err == nil {
			return number
		}
	}
	return 0
}

// ifNoneMatchHit 判断 If-None-Match 是否命中当前 tag（支持多值、弱校验与 *）。
func ifNoneMatchHit(header, tag string) bool {
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		part = strings.Trim(part, `"`)
		if part == "*" || (part != "" && part == tag) {
			return true
		}
	}
	return false
}

// boxsetPoster 解析合集相关 id（boxsets 媒体库 / boxset:<b64>）并返回代表海报路径。
func (a *App) boxsetPoster(rawID string) string {
	if rawID == boxsetViewID {
		// 合集文件夹的封面要逐个合集试到第一个有海报的，缓存住避免每张图都扫一遍合集。
		key := "boxcover:" + a.db.Version("g:version")
		if raw, ok := a.imgMeta.Get(key); ok {
			return string(raw)
		}
		poster := a.firstCollectionPoster(a.cachedCollections())
		a.imgMeta.Set(key, []byte(poster), 5*time.Minute)
		return poster
	}
	var names []string
	if name, ok := parseBoxsetID(rawID); ok {
		names = append(names, name)
	}
	for _, name := range names {
		if poster, _ := a.db.CollectionPoster(name); poster != "" {
			return poster
		}
	}
	return ""
}

func (a *App) imageInfo(c *gin.Context) {
	rawID := c.Param("id")
	if id, err := strconv.ParseInt(rawID, 10, 64); err == nil {
		if libID, ok := parseLibraryExternal(id); ok {
			if a.canViewLibrary(c, libID) {
				if poster := a.libraryCoverPathByID(libID); poster != "" {
					c.JSON(http.StatusOK, []gin.H{{"ImageType": "Primary", "Path": poster, "Filename": filepath.Base(poster)}})
					return
				}
			}
			apiError(c, http.StatusNotFound, "not found")
			return
		}
		movie, err := a.db.Movie(id)
		if err == nil {
			if !movie.IsVisible() || !a.canViewMovie(c, movie) {
				apiError(c, http.StatusNotFound, "not found")
				return
			}
			c.JSON(http.StatusOK, a.movieImageInfo(movie))
			return
		}
		if !store.NotFound(err) {
			itemDataUnavailable(c)
			return
		}
		// Only use the legacy library-id fallback if no movie occupies that id.
		if a.canViewLibrary(c, id) {
			if poster := a.libraryCoverPathByID(id); poster != "" {
				c.JSON(http.StatusOK, []gin.H{{"ImageType": "Primary", "Path": poster, "Filename": filepath.Base(poster)}})
				return
			}
		}
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	images := []gin.H{}
	if name, isBoxset := parseBoxsetID(rawID); isBoxset || rawID == boxsetViewID {
		stats, ok := a.collectionStats(c, 0)
		if !ok {
			return
		}
		poster := ""
		if isBoxset {
			stat, exists := stats[name]
			if !exists {
				apiError(c, http.StatusNotFound, "not found")
				return
			}
			poster = stat.Poster
		} else {
			// Stable representative from visible collections only.
			names := slices.Sorted(maps.Keys(stats))
			for _, name := range names {
				if stats[name].Poster != "" {
					poster = stats[name].Poster
					break
				}
			}
		}
		if poster != "" {
			images = append(images, gin.H{"ImageType": "Primary", "Path": poster, "Filename": filepath.Base(poster), "ImageTag": a.posterTag(poster)})
		}
		c.JSON(http.StatusOK, images)
		return
	}
	if kind, name, ok := entityKind(rawID); ok {
		poster, exists, ok := a.visibleEntity(c, kind, name)
		if !ok {
			return
		}
		if !exists {
			apiError(c, http.StatusNotFound, "not found")
			return
		}
		path, tag := "", ""
		if kind == "Person" {
			path, tag = a.personAvatar(name)
		}
		if path == "" && poster != "" {
			path, tag = poster, a.posterTag(poster)
		}
		if path != "" {
			images = append(images, gin.H{"ImageType": "Primary", "Path": path, "Filename": filepath.Base(path), "ImageTag": tag})
		}
	}
	c.JSON(http.StatusOK, images)
}

// movieImageInfo 组装与真实 Emby 一致的图片清单：
// poster→Primary、landscape→Thumb、fanart→Backdrop（首个 index 0）。
func (a *App) movieImageInfo(m store.Movie) []gin.H {
	images := make([]gin.H, 0, 3)
	for _, item := range []struct {
		imageType string
		path      string
	}{
		{"Primary", m.PosterPath},
		{"Thumb", m.LandscapePath},
		{"Backdrop", m.BackdropPath},
	} {
		if item.path == "" {
			continue
		}
		// 兼容 3.5.2 的 ImageInfo 契约：带 ImageTag（真机 4.9 列表无此键，多给无害）。
		info := gin.H{"ImageType": item.imageType, "Path": item.path, "Filename": filepath.Base(item.path), "ImageTag": a.posterTag(item.path)}
		if item.imageType == "Backdrop" {
			info["ImageIndex"] = 0
		}
		images = append(images, info)
	}
	return images
}

func (a *App) playback(c *gin.Context) {
	movie, sourcePath, id, ok := a.resolvePlaybackTarget(c.Param("id"))
	if !ok || !movie.IsVisible() || !a.canViewMovie(c, movie) || (movie.SourceProtocol != "http" && movie.SourceProtocol != "https") {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"MediaSources":  []gin.H{a.mediaSourceFor(movie, id, sourcePath, c)},
		"PlaySessionId": randomSessionID(),
	})
}

func (a *App) resolvePlaybackTarget(rawID string) (store.Movie, string, string, bool) {
	if movieID, part, ok := parseVirtualPartID(rawID); ok {
		movie, err := a.db.Movie(movieID)
		if err != nil || part-2 >= len(movie.AdditionalParts) {
			return store.Movie{}, "", "", false
		}
		return movie, movie.AdditionalParts[part-2], rawID, true
	}
	movieID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return store.Movie{}, "", "", false
	}
	movie, err := a.db.Movie(movieID)
	if err != nil {
		return store.Movie{}, "", "", false
	}
	return movie, movie.SourcePath, rawID, true
}

// mediaSource 构造 Emby 契约下的 MediaSource。
// Path 是 .strm 在服务器上的文件路径，DirectStreamUrl 是指向本服务流端点、
// 由该端点 302 直拉真实地址的相对路径——两者含义不同，客户端各取所需。
func (a *App) mediaSource(m store.Movie, c *gin.Context) gin.H {
	return a.mediaSourceFor(m, strconv.FormatInt(m.ID, 10), m.SourcePath, c)
}

func (a *App) mediaSourceFor(m store.Movie, id, sourcePath string, c *gin.Context) gin.H {
	stream := streamURL(id)
	mediaSourceId := "mediasource_" + id
	directStream := authenticatedStreamURL(stream+"?MediaSourceId="+mediaSourceId+"&Static=true", requestToken(c))
	// 主文件取 NFO 的 streamdetails，分段取它自己的 mediainfo.json；
	// 两者都没有时才给通用视频轨。
	streams, size := a.streamsFor(m, sourcePath)
	// Path 给的是 .strm 在服务器文件系统里的路径，与真实 Emby 一致
	// （真实 Emby 的 MediaSource.Path 也是服务器绝对路径）。
	// 可播放地址在 DirectStreamUrl：客户端若直接拿 Path 当 URL 用会取不到内容，
	// 但 iPlay 只在 Path 以 http 开头时才用它播放（isUseStrmFirst），
	// 真实 Emby 本身也返回本地路径，故这是符合契约的行为。
	// sourcePath 缺失时回退到流地址，避免给出空值。
	mediaPath := sourcePath
	if strings.TrimSpace(mediaPath) == "" {
		mediaPath = directStream
	}
	source := gin.H{
		"Id":                         mediaSourceId,
		"Name":                       mediaSourceName(m, sourcePath),
		"Path":                       mediaPath,
		"DirectStreamUrl":            directStream,
		"Protocol":                   "Http",
		"Container":                  m.SourceContainer,
		"IsRemote":                   true,
		"HasMixedProtocols":          false,
		"Type":                       "Default",
		"RunTimeTicks":               m.RuntimeSeconds * 10000000,
		"SupportsTranscoding":        false,
		"SupportsDirectStream":       true,
		"SupportsDirectPlay":         true,
		"SupportsProbing":            false,
		"RequiresOpening":            false,
		"RequiresClosing":            false,
		"RequiresLooping":            false,
		"IsInfiniteStream":           false,
		"ItemId":                     id,
		"AddApiKeyToDirectStreamUrl": false,
		"MediaStreams":               streams,
		"DefaultAudioStreamIndex":    -1,
		"DefaultSubtitleStreamIndex": -1,
		// 真实 Emby 恒返回的固定形状字段：本服务无附加容器格式、无需附加请求头。
		// Chapters 恒给空数组：openemby_tv 等客户端按可选字段读它做跳过片头，
		// 数组存在但为空比缺字段更省事（部分客户端的 Gson 反序列化对 null 敏感）。
		"Chapters":              []gin.H{},
		"Formats":               []string{},
		"RequiredHttpHeaders":   gin.H{},
		"ReadAtNativeFramerate": false,
	}
	// MediaSourceInfo.Bitrate 是容器总码率：由各轨码率相加得到（NFO 不单存总码率）。
	if total := streamsBitrate(streams); total > 0 {
		source["Bitrate"] = total
	}
	// Size 来自探测写入 NFO 的 <fileinfo><size>；未探测过的条目无从得知，故缺省。
	if size > 0 {
		source["Size"] = size
	}
	for _, item := range streams {
		if item["Type"] == "Audio" {
			if index, ok := item["Index"].(int); ok {
				source["DefaultAudioStreamIndex"] = index
				break
			}
		}
	}
	return source
}

// authenticatedStreamURL 让不发送自定义鉴权头的播放器也能直接使用返回地址。
func authenticatedStreamURL(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := u.Query()
	query.Set("api_key", token)
	u.RawQuery = query.Encode()
	return u.String()
}

// respondMediaJSON 的输入是共享缓存中的无凭据 DTO；只在发给当前会话时补凭据。
// 不把 token 写回缓存，否则详情及 Fields=MediaSources 列表会泄露其它会话的凭据。
func respondMediaJSON(c *gin.Context, body []byte) {
	if !bytes.Contains(body, []byte(`"MediaSources"`)) {
		c.Data(http.StatusOK, "application/json", body)
		return
	}
	var payload gin.H
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		apiError(c, http.StatusInternalServerError, "invalid media response")
		return
	}
	token := requestToken(c)
	attach := func(item map[string]any) {
		sources, _ := item["MediaSources"].([]any)
		for _, entry := range sources {
			source, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			rawURL, _ := source["DirectStreamUrl"].(string)
			if rawURL == "" {
				continue
			}
			source["DirectStreamUrl"] = authenticatedStreamURL(rawURL, token)
			if path, _ := source["Path"].(string); path == rawURL {
				source["Path"] = source["DirectStreamUrl"]
			}
		}
	}
	attach(payload)
	items, _ := payload["Items"].([]any)
	for _, entry := range items {
		if item, ok := entry.(map[string]any); ok {
			attach(item)
		}
	}
	c.JSON(http.StatusOK, payload)
}

// mediaSourceName 返回 MediaSource 的展示名。真实 Emby 用媒体文件名（去扩展名），
// 而非影片标题；本服务的媒体是 .strm 指向的远程直链，故取直链的文件名，取不到回退标题。
func mediaSourceName(m store.Movie, sourcePath string) string {
	if sourcePath == "" {
		sourcePath = m.SourcePath
	}
	if base := mediaFileName(sourcePath); base != "" {
		return base
	}
	return m.Title
}

// itemFileName 返回条目对应的文件名（含扩展名），即 .strm 文件名。
func itemFileName(m store.Movie) string {
	return filepath.Base(m.SourcePath)
}

// mediaFileName 从 .strm 内容里解析出媒体文件名（去扩展名）。
// 内部只读一次首行，失败返回空串。
func mediaFileName(strmPath string) string {
	raw, err := scanner.ReadSource(strmPath)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if name == "" || name == "." || name == "/" {
		return ""
	}
	if ext := path.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

// streamsBitrate 累加各轨 BitRate 作为容器总码率。
func streamsBitrate(streams []gin.H) int64 {
	var total int64
	for _, item := range streams {
		switch value := item["BitRate"].(type) {
		case int64:
			total += value
		case int:
			total += int64(value)
		}
	}
	return total
}

// streamsFor 返回指定 .strm 的流信息与体积。
//
// 主文件读影片 NFO（Emby/Kodi 的约定位置）；分段读它自己的 mediainfo.json——
// 一个 NFO 只能描述主文件，分段的技术参数只可能存在于各自的探测缓存里。
// 缺缓存时回退主文件的 NFO，保证客户端至少能看到影片级信息而不是空白。
func (a *App) streamsFor(m store.Movie, strmPath string) ([]gin.H, int64) {
	if strmPath == "" || strmPath == m.SourcePath {
		return a.nfoFileInfo(m)
	}
	if file, ok := readMediaInfoFile(strmPath); ok {
		if info, err := infoFromMediaInfo(file); err == nil {
			return buildStreams(streamDetailsFromProbe(info)), info.SizeBytes
		}
	}
	return a.nfoFileInfo(m)
}

// genericVideoStream 未探测（或 NFO 没有可用流信息）时的回退轨。
// 仅驱动直连播放决策，不伪造具体参数。
func genericVideoStream() []gin.H {
	return []gin.H{{"Type": "Video", "Index": 0, "IsDefault": true, "IsForced": false, "IsExternal": false}}
}

// nfoFileInfo 一次解析出 NFO 的流信息与媒体体积（Size 供 MediaSource.Size 使用）。
func (a *App) nfoFileInfo(m store.Movie) ([]gin.H, int64) {
	entry := a.nfoEntry(m)
	return entry.streams, entry.size
}

// nfoCacheMaxEntries NFO 缓存条目上限。缓存按「路径 + 文件 mtime」存，
// 文件每次被改写都会留下一条新条目，到上限时整体清空（而不是 LRU：这里只需要防泄漏）。
const nfoCacheMaxEntries = 20000

// nfoEntry 读取并缓存 NFO 的流信息、体积与「是否已探测」。
//
// 缓存键是「NFO 路径 + 文件 tag（mtime）」，不是纯路径 + 固定 TTL：
// 列表页带上 MediaSources 时一部片就要读一个 NFO，媒体盘慢的时候
// 「100 部片 2.6 秒」几乎全是这些读盘，而 2 分钟 TTL 一到期就重来一遍。
// 用 mtime 做键之后，文件没变就一直命中，外部改了 NFO（mtime 变）立刻重读。
// tag 本身有 5 分钟进程内缓存，命中路径不会每次都 stat 媒体盘。
func (a *App) nfoEntry(m store.Movie) nfoCacheEntry {
	fallback := nfoCacheEntry{streams: genericVideoStream()}
	if m.NFOPath == "" {
		return fallback
	}
	key := m.NFOPath + "|" + a.posterTag(m.NFOPath)
	a.nfoMu.Lock()
	if e, ok := a.nfos[key]; ok {
		a.nfoMu.Unlock()
		return e
	}
	a.nfoMu.Unlock()

	entry := fallback
	entry.ts, entry.neg = time.Now(), true
	meta, err := nfo.Read(m.NFOPath)
	if err == nil && meta.FileInfo != nil {
		entry.size = meta.FileInfo.Size
		if details := meta.FileInfo.StreamDetails; details != nil {
			if out := buildStreams(details); len(out) > 0 {
				entry.streams, entry.probed, entry.neg = out, true, false
			}
		}
	}
	a.nfoMu.Lock()
	if len(a.nfos) >= nfoCacheMaxEntries {
		a.nfos = make(map[string]nfoCacheEntry)
	}
	a.nfos[key] = entry
	a.nfoMu.Unlock()
	return entry
}

// buildStreams 把 NFO 的 streamdetails 映射为 Emby 的 MediaStream 数组。
// 独立成函数：主文件走 NFO、分段走各自的 mediainfo.json，两条来源共用同一套映射规则。
func buildStreams(details *nfo.StreamDetails) []gin.H {
	out := make([]gin.H, 0, 2)
	index := 0
	if video := details.Video; video != nil {
		stream := gin.H{"Type": "Video", "Index": index, "IsDefault": isDefaultTrue(video.Default), "IsForced": isExplicitTrue(video.Forced)}
		setIfNonEmpty(stream, "Codec", video.Codec)
		setIfNonEmpty(stream, "CodecTag", video.CodecTag)
		setIfNonEmpty(stream, "Profile", video.Profile)
		setIfNonEmpty(stream, "PixelFormat", video.PixelFormat)
		setIfNonEmpty(stream, "AspectRatio", video.AspectRatio)
		setIfNonEmpty(stream, "Language", video.Language)
		setIfNonEmpty(stream, "ScanType", video.ScanType)
		setIfNonEmpty(stream, "ColorTransfer", video.ColorTransfer)
		setIfNonEmpty(stream, "ColorPrimaries", video.ColorPrimaries)
		setIfNonEmpty(stream, "ColorSpace", video.ColorSpace)
		setIfNonEmpty(stream, "VideoRange", videoRange(video))
		// DisplayTitle 是客户端展示轨道的首选字段（如 "1080p HEVC"）。
		// 规则按真实 Emby 实测：分辨率标签 + [非 SDR 的 VideoRange] + Codec，不含 Profile。
		setIfNonEmpty(stream, "DisplayTitle", videoDisplayTitle(video))
		setIfNonEmpty(stream, "DisplayLanguage", displayLanguage(video.Language))
		// 以下布尔/枚举字段真实 Emby 恒返回，客户端也会无条件读取，故给出确定值而非缺省。
		stream["IsExternal"] = false
		stream["IsHearingImpaired"] = false
		stream["IsTextSubtitleStream"] = false
		stream["SupportsExternalStream"] = false
		stream["Protocol"] = "Http" // 本服务媒体源均为远程 http(s)（.strm 指向直链）
		stream["AttachmentSize"] = 0
		setIfNonEmpty(stream, "ColorRange", video.ColorRange)
		if video.Width > 0 && video.Height > 0 {
			stream["IsAnamorphic"] = isAnamorphic(video.Width, video.Height, video.AspectRatio)
		}
		if kind, sub, desc := extendedVideo(videoRange(video)); kind != "" {
			stream["ExtendedVideoType"] = kind
			stream["ExtendedVideoSubType"] = sub
			stream["ExtendedVideoSubTypeDescription"] = desc
		}
		if video.Bitrate > 0 {
			stream["BitRate"] = video.Bitrate
		}
		if video.Width > 0 {
			stream["Width"] = video.Width
		}
		if video.Height > 0 {
			stream["Height"] = video.Height
		}
		if video.Level > 0 {
			stream["Level"] = video.Level
		}
		if video.BitDepth > 0 {
			stream["BitDepth"] = video.BitDepth
		}
		if video.RefFrames > 0 {
			stream["RefFrames"] = video.RefFrames
		}
		if video.Framerate > 0 {
			// Emby 契约里 RealFrameRate 是解码帧率、AverageFrameRate 是平均帧率；
			// NFO 只留了一个值，两个都填以免客户端按不同字段取值时拿到空。
			stream["AverageFrameRate"] = video.Framerate
			stream["RealFrameRate"] = video.Framerate
		}
		if video.ScanType != "" {
			stream["IsInterlaced"] = strings.EqualFold(video.ScanType, "interlaced")
		}
		out = append(out, stream)
		index++
	}
	if audio := details.Audio; audio != nil {
		stream := gin.H{"Type": "Audio", "Index": index, "IsDefault": isDefaultTrue(audio.Default), "IsForced": isExplicitTrue(audio.Forced)}
		setIfNonEmpty(stream, "Codec", audio.Codec)
		setIfNonEmpty(stream, "CodecTag", audio.CodecTag)
		setIfNonEmpty(stream, "Profile", audio.Profile)
		setIfNonEmpty(stream, "Language", audio.Language)
		setIfNonEmpty(stream, "ChannelLayout", audio.ChannelLayout)
		setIfNonEmpty(stream, "DisplayTitle", audioDisplayTitle(audio))
		setIfNonEmpty(stream, "DisplayLanguage", displayLanguage(audio.Language))
		// 真实 Emby 对音轨同样返回这些字段（非仅视频轨），客户端会统一读取。
		stream["IsExternal"] = false
		stream["IsHearingImpaired"] = false
		stream["IsTextSubtitleStream"] = false
		stream["SupportsExternalStream"] = false
		stream["IsInterlaced"] = false
		stream["Protocol"] = "Http"
		stream["AttachmentSize"] = 0
		stream["ExtendedVideoType"] = "None"
		stream["ExtendedVideoSubType"] = "None"
		stream["ExtendedVideoSubTypeDescription"] = "None"
		if audio.Bitrate > 0 {
			stream["BitRate"] = audio.Bitrate
		}
		if audio.Channels > 0 {
			stream["Channels"] = audio.Channels
		}
		if audio.SamplingRate > 0 {
			stream["SampleRate"] = audio.SamplingRate
		}
		out = append(out, stream)
		index++
	}
	for _, subtitle := range details.Subtitles {
		stream := gin.H{
			"Type": "Subtitle", "Index": index,
			"IsDefault": isExplicitTrue(subtitle.Default),
			// 强迫字幕必须显式标注：缺省当「非强迫」，否则客户端会把普通字幕当强迫字幕自动烧进画面。
			"IsForced": isExplicitTrue(subtitle.Forced),
			// 外挂字幕的文件不由本服务转发，如实标记，避免客户端去取不存在的地址。
			"IsExternal":             isExplicitTrue(subtitle.External),
			"IsHearingImpaired":      isExplicitTrue(subtitle.HearingImpaired),
			"IsTextSubtitleStream":   isTextSubtitleCodec(subtitle.Codec),
			"SupportsExternalStream": false,
			"Protocol":               "Http",
			"AttachmentSize":         0,
		}
		setIfNonEmpty(stream, "Codec", subtitle.Codec)
		setIfNonEmpty(stream, "CodecTag", subtitle.CodecTag)
		setIfNonEmpty(stream, "Language", subtitle.Language)
		setIfNonEmpty(stream, "DisplayTitle", firstNonEmpty(subtitle.Title, subtitle.Language))
		setIfNonEmpty(stream, "DisplayLanguage", displayLanguage(subtitle.Language))
		out = append(out, stream)
		index++
	}
	return out
}

// isTextSubtitleCodec 判断字幕是否为文本格式（相对图形字幕如 PGS/VOBSUB，
// 文本字幕可转成外挂 srt 由客户端渲染）。未知编码保守判为文本。
func isTextSubtitleCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "pgs", "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub":
		return false
	case "":
		return true
	}
	return true
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// videoDisplayTitle 生成视频轨展示标题。规则取自真实 Emby 实测：
//
//	组件 = 分辨率标签 + [VideoRange（仅非 SDR 且已知）] + Codec 大写
//
// 实测样例："1080p H264"（Profile=Main 不出现在标题里）、"4K HDR 10 HEVC"。
// 注意 Profile 不参与——Emby 不把 Main/Main 10 放进标题。
func videoDisplayTitle(video *nfo.VideoStream) string {
	parts := make([]string, 0, 3)
	if label := resolutionLabel(video.Width, video.Height); label != "" {
		parts = append(parts, label)
	}
	if rng := videoRange(video); rng != "" && rng != "SDR" {
		parts = append(parts, rng)
	}
	if video.Codec != "" {
		parts = append(parts, strings.ToUpper(video.Codec))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

// audioDisplayTitle 生成音轨展示标题。实测样例：
//
//	"Japanese AAC stereo (默认)"、"English TRUEHD 7.1 (默认)"、"English DTS-HD MA 7.1"
//
// 规则：DisplayLanguage + 编码显示名 + ChannelLayout + 默认轨的 " (默认)" 标记。
func audioDisplayTitle(audio *nfo.AudioStream) string {
	parts := make([]string, 0, 4)
	if language := displayLanguage(audio.Language); language != "" {
		parts = append(parts, language)
	}
	if name := audioCodecDisplayName(audio.Codec, audio.Profile); name != "" {
		parts = append(parts, name)
	}
	if audio.ChannelLayout != "" {
		parts = append(parts, audio.ChannelLayout)
	} else if audio.Channels > 0 {
		parts = append(parts, strconv.Itoa(audio.Channels)+"ch")
	}
	if len(parts) == 0 {
		return ""
	}
	title := strings.Join(parts, " ")
	if isDefaultTrue(audio.Default) {
		title += " (默认)"
	}
	return title
}

// audioCodecDisplayName 返回编码的展示名。
// 实测 Emby 对 DTS 系把 Profile 当作编码名的一部分（Profile="DTS-HD MA" →
// 标题里的 "DTS-HD MA"），而 AAC 的 Profile（LC）则被丢弃；此处按此规则处理。
func audioCodecDisplayName(codec, profile string) string {
	codec = strings.TrimSpace(codec)
	profile = strings.TrimSpace(profile)
	if codec == "" {
		return ""
	}
	if strings.EqualFold(codec, "dts") && profile != "" {
		return strings.ToUpper(profile)
	}
	return strings.ToUpper(codec)
}

// resolutionLabel 把像素尺寸换算为 Emby 的展示档位。
// 实测 2160 → "4K"（不是 2160p），1080 → "1080p"。
func resolutionLabel(width, height int) string {
	if height <= 0 {
		if width <= 0 {
			return ""
		}
		return strconv.Itoa(width) + "x?"
	}
	switch {
	case height >= 1700: // 2160p / 1440p 等 UHD 档
		return "4K"
	case height >= 1000:
		return "1080p"
	case height >= 700:
		return "720p"
	case height >= 560:
		return "576p"
	case height >= 460:
		return "480p"
	case height >= 340:
		return "360p"
	case height >= 200:
		return "240p"
	}
	return strconv.Itoa(height) + "p"
}

// videoRange 依据色度传输特性判定动态范围，取值与真实 Emby 一致：
// "SDR" / "HDR 10" / "HLG"。
// 不能用位深推断——10bit SDR 片源真实存在，凭位深判 HDR 会误标；
// 拿不到传输特性时返回空串（宁可不报，也不给错值）。
func videoRange(video *nfo.VideoStream) string {
	switch strings.ToLower(strings.TrimSpace(video.ColorTransfer)) {
	case "smpte2084", "bt2020-10", "bt2020-12": // PQ，即 HDR10
		return "HDR 10"
	case "arib-std-b67": // HLG
		return "HLG"
	case "bt709", "bt470bg", "smpte170m", "smpte240m", "iec61966-2-1", "iec61966-2-4":
		return "SDR"
	}
	// 传输特性缺失时退一步看基色：BT.2020 属于广色域，按 HDR 报。
	if strings.EqualFold(strings.TrimSpace(video.ColorPrimaries), "bt2020") {
		return "HDR 10"
	}
	return ""
}

// extendedVideo 返回 ExtendedVideoType/SubType/SubTypeDescription 三元组。
// 真实 Emby 对 SDR 轨给 "None"，对 HDR10 给 "Hdr10"；未知范围时整体缺省。
func extendedVideo(openRange string) (string, string, string) {
	switch openRange {
	case "SDR":
		return "None", "None", "None"
	case "HDR 10":
		return "Hdr10", "Hdr10", "HDR 10"
	case "HLG":
		return "Hlg", "Hlg", "HLG"
	}
	return "", "", ""
}

// isAnamorphic 判定是否变形宽银幕：编码像素比与显示宽高比不一致。
// NFO 的 aspectratio 通常就是显示比例，两者不等即为 anamorphic。
// 无法解析显示比例时返回 false（与 Emby 对非变形源的默认一致）。
func isAnamorphic(width, height int, aspectRatio string) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	raw := strings.TrimSpace(aspectRatio)
	if raw == "" {
		return false
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return false
	}
	displayWidth, errW := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	displayHeight, errH := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errW != nil || errH != nil || displayWidth <= 0 || displayHeight <= 0 {
		return false
	}
	display := displayWidth / displayHeight
	coded := float64(width) / float64(height)
	// 允许 2% 误差，避免 1440x1080 这类整数比在浮点下误判。
	return math.Abs(display-coded)/display > 0.02
}

// displayLanguage 把 ISO 639-2 语言码转成展示名；未知码原样返回。
func displayLanguage(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "und", "":
		return "" // 未定义语言不展示，避免客户端显示 "und"
	case "jpn":
		return "Japanese"
	case "eng":
		return "English"
	case "chi", "zho":
		return "Chinese"
	case "kor":
		return "Korean"
	}
	return code
}

// isDefaultTrue 判断「是否默认轨」。NFO 里 <default> 缺省即视为默认——
// 若一条轨都不标默认，客户端可能拒绝播放，故缺省取 true。
func isDefaultTrue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "true", "1", "yes":
		return true
	}
	return false
}

// isExplicitTrue 判断「显式标注为真」。用于两类字段：
//   - IsForced：缺省当强迫会让客户端自动烧字幕/强制选轨，必须显式才算；
//   - 字幕轨的 IsDefault：缺省当默认会让多条字幕同时声称默认（且客户端会自动开字幕），
//     与音视频轨相反——音视频轨缺省取 true 见 isDefaultTrue。
func isExplicitTrue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

func setIfNonEmpty(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// streamURL 返回指向本服务流端点的**相对路径**（不含主机、不含 /emby 前缀）。
//
// 必须相对且不带前缀，这是参考客户端写死的行为：
//   - openemby_tv 用 `${serverUrl}/emby$path` 拼接（serverUrl 不含 /emby），
//     返回绝对地址或带前缀的路径都会拼出 `.../embyhttp://...`、`/emby/emby/...`；
//   - iPlay 的 buildUrl 只在「不以 http 开头」时才把自己的 base 拼上去；
//   - iPlay 鸿蒙版直接 `server + DirectStreamUrl` 拼接。
//
// 相对地址同时避开了反向代理场景下的主机误判（服务看到的 Host 常是内网地址），
// 也让同一份响应可以被所有宿主共用（缓存不必再按请求来源分桶）。
func streamURL(id string) string {
	return "/Videos/" + id + "/stream"
}

func randomSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (a *App) stream(c *gin.Context) {
	movie, sourcePath, _, ok := a.resolvePlaybackTarget(c.Param("id"))
	if !ok || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	raw, err := scanner.ReadSource(sourcePath)
	if err != nil {
		apiError(c, http.StatusNotFound, "source not found")
		return
	}
	if !scanner.ValidHTTP(raw) {
		apiError(c, http.StatusUnsupportedMediaType, "unsupported source")
		return
	}
	if c.Request.Method == http.MethodHead {
		c.Header("Location", raw)
		c.Status(http.StatusFound)
		return
	}
	http.Redirect(c.Writer, c.Request, raw, http.StatusFound)
}

// streamClient 供网页播放器代理播放使用：不设总超时（长连接），由请求 context 控制取消。
// 播放地址来自用户配置的 strm，局域网/回环是正常用法，因此允许私网；
// 链路本地（含云元数据）、组播等地址仍由 internal/outbound 拒绝。
var streamClient = outbound.NewClient(outbound.Options{
	AllowPrivate:          true,
	Timeout:               0,
	MaxConnsPerHost:       32,
	ResponseHeaderTimeout: 30 * time.Second,
})

// proxyStream 为内置网页播放器代理真实媒体地址：透传 Range/状态码/关键响应头。
// 解决 HTTPS 后台 + HTTP 源站的混合内容、跨域/防盗链导致的播放失败。
// Emby 客户端仍走 /stream 的 302 直拉（服务端零带宽），此端点只服务网页播放器。
func (a *App) proxyStream(c *gin.Context) {
	movie, sourcePath, _, ok := a.resolvePlaybackTarget(c.Param("id"))
	if !ok || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	raw, err := scanner.ReadSource(sourcePath)
	if err != nil {
		apiError(c, http.StatusNotFound, "source not found")
		return
	}
	if !scanner.ValidHTTP(raw) {
		apiError(c, http.StatusUnsupportedMediaType, "unsupported source")
		return
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, raw, nil)
	if err != nil {
		// 不回显上游地址：错误里可能带凭据或内网主机名。
		c.JSON(http.StatusBadGateway, gin.H{"error": "playback source unavailable", "code": "UPSTREAM_UNAVAILABLE"})
		return
	}
	if rng := c.GetHeader("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	// 必须禁用压缩：否则 transport 自动解压会让 Content-Length/Content-Range 失真、拖动失效。
	req.Header.Set("Accept-Encoding", "identity")
	if ua := c.GetHeader("User-Agent"); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		slog.Debug("proxy stream upstream failed", "movie_id", movie.ID)
		c.JSON(http.StatusBadGateway, gin.H{"error": "playback source unavailable", "code": "UPSTREAM_UNAVAILABLE"})
		return
	}
	defer resp.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if value := resp.Header.Get(header); value != "" {
			c.Header(header, value)
		}
	}
	if c.Writer.Header().Get("Accept-Ranges") == "" {
		c.Header("Accept-Ranges", "bytes")
	}
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Header("Content-Type", containerMIME(movie.SourceContainer))
	}
	c.Status(resp.StatusCode)
	if c.Request.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(c.Writer, resp.Body)
}

// containerMIME 给代理播放一个兜底 Content-Type（源站未给时用）。
func containerMIME(container string) string {
	switch strings.ToLower(container) {
	case "mp4", "m4v":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	case "mov":
		return "video/quicktime"
	case "ts":
		return "video/mp2t"
	}
	return "application/octet-stream"
}

func (a *App) playing(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		apiError(c, http.StatusBadRequest, "invalid playback payload")
		return
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		c.Status(http.StatusNoContent)
		return
	}
	var req struct {
		ItemId        string `json:"ItemId"`
		PositionTicks int64  `json:"PositionTicks"`
		PlaySessionId string `json:"PlaySessionId"`
		DeviceId      string `json:"DeviceId"`
		EventName     string `json:"EventName"`
		IsPaused      bool   `json:"IsPaused"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		apiError(c, http.StatusBadRequest, "invalid playback payload")
		return
	}
	movie, _, _, ok := a.resolvePlaybackTarget(req.ItemId)
	if !ok || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	// 当前用户状态以主影片为单位；合法分段共享主影片记录，进度保持分段内相对位置。
	id := movie.ID
	// 播放计数由 D07 会话/事件协议保证一次播放只计一次（迁移 2）：
	// start/progress/stopped 都进同一事务；重复 Stopped 与停止后的迟到心跳
	// 不会再改计数或覆盖停止标记，也不能靠重放同一位置来回滚 play_count。
	kind := store.PlaybackStart
	switch strings.ToLower(filepath.Base(c.Request.URL.Path)) {
	case "progress":
		kind = store.PlaybackProgress
	case "stopped":
		kind = store.PlaybackStopped
	}
	isStopped := kind == store.PlaybackStopped
	result, err := a.db.RecordPlaybackEvent(store.PlaybackEvent{
		SessionKey:    req.PlaySessionId,
		UserID:        a.currentUserID(c),
		MovieID:       id,
		DeviceID:      req.DeviceId,
		Kind:          kind,
		PositionTicks: req.PositionTicks,
		Now:           time.Now(),
	})
	if err != nil {
		apiError(c, 500, err.Error())
		return
	}
	// The playback transaction has committed. A later cache-version failure must
	// not suppress an accepted stop that cannot be replayed as a new event.
	if result.Accepted {
		a.notifyPlaybackWebhook(c, movie, kind, req.EventName, req.PlaySessionId, req.DeviceId, req.PositionTicks, req.IsPaused)
	}
	// 进度心跳（Progress）几秒一次，若每次都 BumpVersion，列表/实体/详情/相似缓存
	// （key 含 g:version）会被反复打掉，等于没有缓存。进度本身只影响进度条，
	// 落一个 TTL 无所谓（列表 15s），代价远小于全站 cache miss 风暴。
	// 因此：只有真正改变归属状态的 Stopped 才提升版本号（全站失效，低频）；
	// 心跳只精确失效该片的详情缓存（它 TTL 1 小时，不失效会长时间显示旧进度）。
	if isStopped {
		if err := a.db.BumpVersion(movie.LibraryID); err != nil {
			apiError(c, 500, err.Error())
			return
		}
	} else {
		a.cache.Delete(movieDetailCacheKey(a.db.Version("g:version"), a.currentUserID(c), strconv.FormatInt(id, 10)))
	}
	c.Status(http.StatusNoContent)
}

func (a *App) setPlayed(c *gin.Context, played bool) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	if err := a.db.SetIndexedPlayedForUser(a.currentUserID(c), id, played); err != nil {
		userDataWriteError(c, err)
		return
	}
	name := "item.markunplayed"
	if played {
		name = "item.markplayed"
	}
	a.notifyMovieDataWebhook(c, name, movie, map[string]any{"Played": played})
	c.Status(http.StatusNoContent)
}

func (a *App) played(c *gin.Context) { a.setPlayed(c, true) }

func (a *App) unplayed(c *gin.Context) { a.setPlayed(c, false) }

func (a *App) additionalParts(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() || !a.canViewMovie(c, movie) {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	items := make([]gin.H, 0, len(movie.AdditionalParts))
	for index := range movie.AdditionalParts {
		items = append(items, a.partItem(movie, index))
	}
	c.JSON(http.StatusOK, gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
}

func virtualPartID(movieID int64, part int) string {
	return "part-" + strconv.FormatInt(movieID, 10) + "-" + strconv.Itoa(part)
}

// partItem 渲染影片某 AdditionalPart 的 BaseItemDto，Id 用虚拟 part-<movieID>-<index+2>，
// 元数据与图片共享主影片，保证 iPlay 点开分段项时详情可取图、不白屏。
func (a *App) partItem(m store.Movie, index int) gin.H {
	part := index + 2
	imageTags := gin.H{}
	backdrops := make([]string, 0, 1)
	if m.PosterPath != "" {
		imageTags["Primary"] = a.posterTag(m.PosterPath)
	}
	if m.LandscapePath != "" {
		imageTags["Thumb"] = a.posterTag(m.LandscapePath)
	} else if m.PosterPath != "" {
		imageTags["Thumb"] = a.posterTag(m.PosterPath)
	}
	if m.BackdropPath != "" {
		tag := a.posterTag(m.BackdropPath)
		backdrops = append(backdrops, tag)
		imageTags["Backdrop"] = tag
	}
	item := gin.H{
		"Id":                virtualPartID(m.ID, part),
		"Name":              m.Title + " - CD" + strconv.Itoa(part),
		"SortName":          m.Title,
		"Type":              "Movie",
		"MediaType":         "Video",
		"ParentId":          strconv.FormatInt(m.ID, 10),
		"ServerId":          a.serverID,
		"Container":         m.SourceContainer,
		"PartCount":         len(m.AdditionalParts) + 1,
		"IsFolder":          false,
		"CanDelete":         false,
		"CanDownload":       false,
		"SupportsSync":      false,
		"ImageTags":         imageTags,
		"BackdropImageTags": backdrops,
	}
	if m.RuntimeSeconds > 0 {
		item["RunTimeTicks"] = m.RuntimeSeconds * 10000000
	}
	return item
}

func parseVirtualPartID(raw string) (int64, int, bool) {
	pieces := strings.Split(raw, "-")
	if len(pieces) != 3 || pieces[0] != "part" {
		return 0, 0, false
	}
	movieID, movieErr := strconv.ParseInt(pieces[1], 10, 64)
	part, partErr := strconv.Atoi(pieces[2])
	return movieID, part, movieErr == nil && part >= 2 && partErr == nil
}

// emptyList 供非官方插件探针端点（IntroSkipper / MediaSegments）返回空数组占位。
func (a *App) emptyList(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{})
}
