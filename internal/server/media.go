package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
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
	// 主文件取 NFO 的 streamdetails，分段取它自己的 mediainfo.json；
	// 两者都没有时才给通用视频轨。
	streams, size := a.streamsFor(m, sourcePath)
	source := gin.H{
		"Id":                         mediaSourceId,
		"Name":                       mediaSourceName(m, sourcePath),
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
		"MediaStreams":               streams,
		"DefaultAudioStreamIndex":    -1,
		"DefaultSubtitleStreamIndex": -1,
		// 真实 Emby 恒返回的固定形状字段：本服务无附加容器格式、无需附加请求头。
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

// nfoFileInfo 一次解析出 NFO 的流信息与媒体体积（Size 供 MediaSource.Size 使用）。
// 读不到流信息时回退一个通用视频轨（仅驱动直连播放决策，不伪造具体参数）。
// 结果按 NFO 路径做进程内短缓存：列表页 MediaSources 大批量请求时避免反复读盘解析。
func (a *App) nfoFileInfo(m store.Movie) ([]gin.H, int64) {
	fallback := []gin.H{{"Type": "Video", "Index": 0, "IsDefault": true, "IsForced": false, "IsExternal": false}}
	if m.NFOPath == "" {
		return fallback, 0
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
			cached, size := e.streams, e.size
			a.nfoMu.Unlock()
			return cached, size
		}
	}
	a.nfoMu.Unlock()

	meta, err := nfo.Read(m.NFOPath)
	if err != nil || meta.FileInfo == nil || meta.FileInfo.StreamDetails == nil {
		var size int64
		if err == nil && meta.FileInfo != nil {
			size = meta.FileInfo.Size
		}
		a.nfoMu.Lock()
		a.nfos[m.NFOPath] = nfoCacheEntry{streams: fallback, size: size, ts: now, neg: true}
		a.nfoMu.Unlock()
		return fallback, size
	}
	details := meta.FileInfo.StreamDetails
	out := buildStreams(details)
	if len(out) == 0 {
		a.nfoMu.Lock()
		a.nfos[m.NFOPath] = nfoCacheEntry{streams: fallback, size: meta.FileInfo.Size, ts: now, neg: true}
		a.nfoMu.Unlock()
		return fallback, meta.FileInfo.Size
	}
	a.nfoMu.Lock()
	a.nfos[m.NFOPath] = nfoCacheEntry{streams: out, size: meta.FileInfo.Size, ts: now}
	a.nfoMu.Unlock()
	return out, meta.FileInfo.Size
}

// buildStreams 把 NFO 的 streamdetails 映射为 Emby 的 MediaStream 数组。
// 独立成函数：主文件走 NFO、分段走各自的 mediainfo.json，两条来源共用同一套映射规则。
func buildStreams(details *nfo.StreamDetails) []gin.H {
	out := make([]gin.H, 0, 2)
	index := 0
	if video := details.Video; video != nil {
		stream := gin.H{"Type": "Video", "Index": index, "IsDefault": isDefaultTrue(video.Default), "IsForced": isDefaultTrue(video.Forced)}
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
		stream := gin.H{"Type": "Audio", "Index": index, "IsDefault": isDefaultTrue(audio.Default), "IsForced": isDefaultTrue(audio.Forced)}
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
	return out
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
	return streamOrigin(c) + "/Videos/" + id + "/stream"
}

// streamOrigin 返回本次请求下媒体流地址的来源前缀（协议 + 主机 + 可选的 /emby 前缀）。
// 含 MediaSources 的响应里带的是绝对流地址，必须用它做缓存分桶：
// 否则先到的宿主（如内网 IP）会把地址缓存下来，再发给其它宿主的请求。
func streamOrigin(c *gin.Context) string {
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
	return scheme + "://" + c.Request.Host + prefix
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
