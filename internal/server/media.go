package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/nfo"
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
				c.File(poster)
				return
			}
			c.Status(404)
			return
		}
		// 数值 id 优先命中影片；影片不存在/不可见时回退为该媒体库的封面。
		m, e := a.db.Movie(id)
		if e == nil && m.IsVisible() {
			if p := movieImagePath(m, kind); p != "" {
				c.File(p)
				return
			}
			c.Status(404)
			return
		}
		if poster := a.libraryCoverPathByID(id); poster != "" && strings.EqualFold(kind, "Primary") {
			c.File(poster)
			return
		}
		c.Status(404)
		return
	}
	// 合集：boxsets 媒体库文件夹用任一合集海报；boxset:<b64> 用该合集海报。
	if p := a.boxsetPoster(rawID); p != "" {
		c.File(p)
		return
	}
	// 虚拟实体封面：任何 ImageType 都回代表性海报，保证 Tag/Genre 网格有图。
	if kind, name, ok := entityKind(rawID); ok {
		if poster := a.entityPosterPath(kind, name); poster != "" {
			c.File(poster)
			return
		}
	}
	c.Status(404)
}

// boxsetPoster 解析合集相关 id（boxsets 媒体库 / boxset:<b64>）并返回代表海报路径。
func (a *App) boxsetPoster(rawID string) string {
	names := []string{}
	if rawID == boxsetViewID {
		names, _ = a.db.Collections()
	} else if name, ok := parseBoxsetID(rawID); ok {
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
			if poster := a.libraryCoverPathByID(libID); poster != "" {
				c.JSON(http.StatusOK, []gin.H{{"ImageType": "Primary", "Path": poster, "Filename": filepath.Base(poster)}})
				return
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		movie, err := a.db.Movie(id)
		if err == nil && movie.IsVisible() {
			c.JSON(http.StatusOK, a.movieImageInfo(movie))
			return
		}
		// 媒体库（旧内部 id 兼容）：给出库封面，避免列表页拿空数组。
		if poster := a.libraryCoverPathByID(id); poster != "" {
			c.JSON(http.StatusOK, []gin.H{{"ImageType": "Primary", "Path": poster, "Filename": filepath.Base(poster)}})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// 合集：仅 Primary（代表海报）。
	if p := a.boxsetPoster(rawID); p != "" {
		c.JSON(http.StatusOK, []gin.H{{"ImageType": "Primary", "Path": p, "Filename": filepath.Base(p), "ImageTag": a.posterTag(p)}})
		return
	}
	// 虚拟实体：仅 Primary（代表性海报）。
	images := []gin.H{}
	if kind, name, ok := entityKind(rawID); ok {
		if poster := a.entityPosterPath(kind, name); poster != "" {
			images = append(images, gin.H{"ImageType": "Primary", "Path": poster, "Filename": filepath.Base(poster), "ImageTag": a.posterTag(poster)})
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
	if !ok || !movie.IsVisible() || (movie.SourceProtocol != "http" && movie.SourceProtocol != "https") {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
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
// Path / DirectStreamUrl 均指向本服务流端点（stream 再 302 直拉真实地址），
// 保证 iPlay 等客户端无论走「strm http path」还是 DirectStreamUrl 都能拿到可播地址。
func (a *App) mediaSource(m store.Movie, c *gin.Context) gin.H {
	return a.mediaSourceFor(m, strconv.FormatInt(m.ID, 10), m.SourcePath, c)
}

func (a *App) mediaSourceFor(m store.Movie, id, sourcePath string, c *gin.Context) gin.H {
	stream := streamURL(c, id)
	mediaSourceId := "mediasource_" + id
	return gin.H{
		"Id":                         mediaSourceId,
		"Name":                       m.Title,
		"Path":                       stream,
		"DirectStreamUrl":            stream + "?MediaSourceId=" + mediaSourceId + "&Static=true",
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
		// MediaStreams 优先取自 NFO <fileinfo><streamdetails>；无 NFO/无流信息时才给通用视频轨。
		"MediaStreams": a.mediaStreams(m),
	}
}

// mediaStreams 从同目录 NFO 的 <fileinfo><streamdetails> 组装真实音视频轨。
// 读不到时回退一个通用视频轨（仅驱动直连播放决策，不伪造具体参数）。
// 结果按 NFO 路径做进程内短缓存：列表页 MediaSources 大批量请求时避免反复读盘解析。
func (a *App) mediaStreams(m store.Movie) []gin.H {
	fallback := []gin.H{{"Type": "Video", "Index": 0, "IsDefault": true, "IsForced": false, "IsExternal": false}}
	if m.NFOPath == "" {
		return fallback
	}
	now := time.Now()
	a.nfoMu.Lock()
	if a.nfos == nil {
		a.nfos = make(map[string]nfoCacheEntry)
	}
	if e, ok := a.nfos[m.NFOPath]; ok {
		ttl := 2 * time.Minute
		if e.neg {
			ttl = 30 * time.Second
		}
		if now.Sub(e.ts) < ttl {
			cached := e.streams
			a.nfoMu.Unlock()
			return cached
		}
	}
	a.nfoMu.Unlock()

	meta, err := nfo.Read(m.NFOPath)
	if err != nil || meta.FileInfo == nil || meta.FileInfo.StreamDetails == nil {
		a.nfoMu.Lock()
		a.nfos[m.NFOPath] = nfoCacheEntry{streams: fallback, ts: now, neg: true}
		a.nfoMu.Unlock()
		return fallback
	}
	details := meta.FileInfo.StreamDetails
	out := make([]gin.H, 0, 2)
	index := 0
	if video := details.Video; video != nil {
		stream := gin.H{"Type": "Video", "Index": index, "IsDefault": isDefaultTrue(video.Default), "IsForced": isDefaultTrue(video.Forced)}
		setIfNonEmpty(stream, "Codec", video.Codec)
		setIfNonEmpty(stream, "CodecTag", video.CodecTag)
		if video.Bitrate > 0 {
			stream["BitRate"] = video.Bitrate
		}
		if video.Width > 0 {
			stream["Width"] = video.Width
		}
		if video.Height > 0 {
			stream["Height"] = video.Height
		}
		setIfNonEmpty(stream, "Language", video.Language)
		setIfNonEmpty(stream, "ScanType", video.ScanType)
		out = append(out, stream)
		index++
	}
	if audio := details.Audio; audio != nil {
		stream := gin.H{"Type": "Audio", "Index": index, "IsDefault": isDefaultTrue(audio.Default), "IsForced": isDefaultTrue(audio.Forced)}
		setIfNonEmpty(stream, "Codec", audio.Codec)
		setIfNonEmpty(stream, "CodecTag", audio.CodecTag)
		if audio.Bitrate > 0 {
			stream["BitRate"] = audio.Bitrate
		}
		setIfNonEmpty(stream, "Language", audio.Language)
		if audio.Channels > 0 {
			stream["Channels"] = audio.Channels
		}
		if audio.SamplingRate > 0 {
			stream["SampleRate"] = audio.SamplingRate
		}
		out = append(out, stream)
		index++
	}
	if len(out) == 0 {
		a.nfoMu.Lock()
		a.nfos[m.NFOPath] = nfoCacheEntry{streams: fallback, ts: now, neg: true}
		a.nfoMu.Unlock()
		return fallback
	}
	a.nfoMu.Lock()
	a.nfos[m.NFOPath] = nfoCacheEntry{streams: out, ts: now}
	a.nfoMu.Unlock()
	return out
}

func isDefaultTrue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "true", "1", "yes":
		return true
	}
	return false
}

func setIfNonEmpty(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// streamURL 生成指向本服务流端点的绝对地址（尊重反向代理前缀与协议头）。
// 客户端经 /emby 前缀访问时返回含前缀的 URL，确保反向代理只暴露 /emby 子路径也够用。
func streamURL(c *gin.Context, id string) string {
	scheme := c.Request.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
	}
	prefix := ""
	if strings.HasPrefix(c.Request.URL.Path, "/emby") {
		prefix = "/emby"
	}
	return scheme + "://" + c.Request.Host + prefix + "/Videos/" + id + "/stream"
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
	if !ok || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	raw, err := scanner.ReadSource(sourcePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	if !scanner.ValidHTTP(raw) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported source"})
		return
	}
	if c.Request.Method == http.MethodHead {
		c.Header("Location", raw)
		c.Status(http.StatusFound)
		return
	}
	http.Redirect(c.Writer, c.Request, raw, http.StatusFound)
}

// streamClient 供网页播放器代理播放使用：不设超时（长连接），由请求 context 控制取消。
var streamClient = &http.Client{}

// proxyStream 为内置网页播放器代理真实媒体地址：透传 Range/状态码/关键响应头。
// 解决 HTTPS 后台 + HTTP 源站的混合内容、跨域/防盗链导致的播放失败。
// Emby 客户端仍走 /stream 的 302 直拉（服务端零带宽），此端点只服务网页播放器。
func (a *App) proxyStream(c *gin.Context) {
	movie, sourcePath, _, ok := a.resolvePlaybackTarget(c.Param("id"))
	if !ok || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	raw, err := scanner.ReadSource(sourcePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	if !scanner.ValidHTTP(raw) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported source"})
		return
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, raw, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playback payload"})
		return
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		c.Status(http.StatusNoContent)
		return
	}
	var req struct {
		ItemId        string `json:"ItemId"`
		PositionTicks int64  `json:"PositionTicks"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playback payload"})
		return
	}
	id, err := strconv.ParseInt(req.ItemId, 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	d, err := a.db.Data(id)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	positionTicks := req.PositionTicks
	stoppedTicks := int64(-1)
	playCount := d.PlayCount
	if strings.HasSuffix(c.Request.URL.Path, "Stopped") {
		stoppedTicks = req.PositionTicks
		if d.StoppedTicks != req.PositionTicks {
			playCount++
		}
	}
	if err := a.db.SavePlayback(id, positionTicks, playCount, time.Now().UTC().Format(time.RFC3339), stoppedTicks); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.BumpVersion(movie.LibraryID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// 不再 Clear：列表/详情缓存 key 含库版本号，BumpVersion 后自然失效；
	// 播放进度每秒上报一次，全量 SCAN 会连带清掉 token 缓存。
	c.Status(http.StatusNoContent)
}

func (a *App) setPlayed(c *gin.Context, played bool) {
	if !a.validUser(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err := a.db.SetPlayed(id, played); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.BumpVersion(movie.LibraryID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *App) played(c *gin.Context) { a.setPlayed(c, true) }

func (a *App) unplayed(c *gin.Context) { a.setPlayed(c, false) }

func (a *App) additionalParts(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil || !movie.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
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
