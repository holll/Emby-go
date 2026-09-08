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
		// 媒体库外部 id：库封面（优先宽图 fanart），与影片 id 不再冲突。
		if libID, ok := parseLibraryExternal(id); ok {
			if poster, e2 := a.db.RepresentativeArt(libID); e2 == nil && poster != "" {
				c.File(poster)
				return
			}
			c.Status(404)
			return
		}
		// 数值 id 优先命中影片；影片不存在/不可见时回退为该媒体库的代表封面。
		m, e := a.db.Movie(id)
		if e == nil && m.IsVisible() {
			if p := movieImagePath(m, kind); p != "" {
				c.File(p)
				return
			}
			c.Status(404)
			return
		}
		if poster, e2 := a.db.RepresentativeArt(id); e2 == nil && poster != "" && strings.EqualFold(kind, "Primary") {
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
			if poster, e2 := a.db.RepresentativeArt(libID); e2 == nil && poster != "" {
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
		// 媒体库（旧内部 id 兼容）：给出 RepresentativeArt（优先宽图 fanart），避免列表页拿空数组。
		if poster, e2 := a.db.RepresentativeArt(id); e2 == nil && poster != "" {
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
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	m, e := a.db.Movie(id)
	if e != nil || !m.IsVisible() || (m.SourceProtocol != "http" && m.SourceProtocol != "https") {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	c.JSON(200, gin.H{
		"MediaSources":  []gin.H{a.mediaSource(m, c)},
		"PlaySessionId": randomSessionID(),
	})
}

// mediaSource 构造 Emby 契约下的 MediaSource。
// Path / DirectStreamUrl 均指向本服务流端点（stream 再 302 直拉真实地址），
// 保证 iPlay 等客户端无论走「strm http path」还是 DirectStreamUrl 都能拿到可播地址。
func (a *App) mediaSource(m store.Movie, c *gin.Context) gin.H {
	id := strconv.FormatInt(m.ID, 10)
	stream := streamURL(c, m.ID)
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
func streamURL(c *gin.Context, id int64) string {
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
	return scheme + "://" + c.Request.Host + prefix + "/Videos/" + strconv.FormatInt(id, 10) + "/stream"
}

func randomSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (a *App) stream(c *gin.Context) {
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
	raw, err := scanner.ReadSource(movie.SourcePath)
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
	d.PositionTicks = req.PositionTicks
	if strings.HasSuffix(c.Request.URL.Path, "Stopped") && d.StoppedTicks != req.PositionTicks {
		d.PlayCount++
		d.StoppedTicks = req.PositionTicks
	} else if !strings.HasSuffix(c.Request.URL.Path, "Stopped") {
		d.StoppedTicks = -1
	}
	d.LastPlayedAt = time.Now().UTC().Format(time.RFC3339)
	if err := a.db.SaveData(id, d); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if err := a.db.BumpVersion(movie.LibraryID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.cache.Clear()
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
	a.cache.Clear()
	c.Status(http.StatusNoContent)
}

func (a *App) played(c *gin.Context) { a.setPlayed(c, true) }

func (a *App) unplayed(c *gin.Context) { a.setPlayed(c, false) }

func (a *App) additionalParts(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"Items": []gin.H{}, "TotalRecordCount": 0, "StartIndex": 0})
}

// emptyList 供非官方插件探针端点（IntroSkipper / MediaSegments）返回空数组占位。
func (a *App) emptyList(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{})
}
