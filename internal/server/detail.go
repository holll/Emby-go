package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/avatar"
	"emby-go/internal/nfo"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

// 管理端单条详情：媒体墙详情抽屉的数据源。
//
// 只读，不含编辑/收藏/标记已看。技术参数复用 sourceMediaInfo()（主文件读 NFO、
// 分段读各自 mediainfo.json），演员复用批量关联读取，不重复实现解析逻辑。

// detailFile 一个媒体文件（主文件或分段）的展示信息。
type detailFile struct {
	Index     int     `json:"index"` // 1 = 主文件，2..n = 分段
	Path      string  `json:"path"`
	Name      string  `json:"name"`
	Role      string  `json:"role"` // main / part
	Size      int64   `json:"size"`
	MediaInfo string  `json:"mediainfo_path,omitempty"`
	Streams   []gin.H `json:"streams"`
	Probed    bool    `json:"probed"`
}

// detailActor 演员及其头像可用性（前端据此决定显示头像还是首字母占位）。
type detailActor struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url,omitempty"`
	ImageTag  string `json:"image_tag,omitempty"`
	HasImage  bool   `json:"has_image"`
}

func (a *App) adminItemDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		if store.NotFound(err) {
			apiError(c, http.StatusNotFound, "not found")
		} else {
			itemDataUnavailable(c)
		}
		return
	}
	data, actorMap, ok := a.itemAssociations(c, []int64{movie.ID})
	if !ok {
		return
	}
	userData, hasUserData := data[movie.ID]
	libraryName := ""
	lib, err := a.db.Library(movie.LibraryID)
	if err != nil && !store.NotFound(err) {
		itemDataUnavailable(c)
		return
	}
	if err == nil {
		libraryName = lib.Name
	}

	paths := append([]string{movie.SourcePath}, movie.AdditionalParts...)
	files := make([]detailFile, 0, len(paths))
	for i, path := range paths {
		entry := a.sourceMediaInfo(movie, path)
		file := detailFile{
			Index: i + 1, Path: path, Name: filepath.Base(path),
			Role: "main", Streams: entry.streams, Probed: entry.probed, Size: entry.size,
		}
		if i != 0 {
			file.Role, file.MediaInfo = "part", mediaInfoPath(path)
			if !entry.probed {
				file.Streams = nil
			}
		} else if entry.stale {
			movie.RuntimeSeconds = entry.runtime
		}
		if file.Size <= 0 {
			file.Size = fileSize(path)
		}
		files = append(files, file)
	}

	actors := make([]detailActor, 0, 8)
	if list := actorMap[movie.ID]; len(list) > 0 {
		dir := a.avatarsDir()
		for _, actor := range list {
			item := detailActor{Name: actor.Name, AvatarURL: actor.AvatarURL}
			// 头像任务只在下载成功后才写 avatar_tag，有 tag 即认为本地有副本，
			// 这里不再逐演员 os.Stat——几十个演员就是几十次磁盘 stat，每次开抽屉都要重来。
			if tag := strings.TrimSpace(actor.AvatarTag); tag != "" {
				if path := avatar.Path(dir, actor.Name); path != "" {
					item.HasImage, item.ImageTag = true, tag
				}
			}
			actors = append(actors, item)
		}
	}

	lockedFields := make([]string, 0)
	lockData := false
	nfoPath := movie.NFOPath
	if nfoPath == "" {
		nfoPath = nfo.SidecarPath(movie.SourcePath, len(movie.AdditionalParts) > 0)
	}
	if meta, err := nfo.Read(nfoPath); err == nil {
		for field := range strings.SplitSeq(meta.LockedFields, "|") {
			if field = strings.TrimSpace(field); field != "" {
				lockedFields = append(lockedFields, field)
			}
		}
		lockData = strings.EqualFold(strings.TrimSpace(meta.LockData), "true")
	}

	// 已保存的 MetaTube 身份（派生自索引里的 provider_id，不读 NFO/磁盘）：
	// canonical 是官方可复用格式，valid 为假时前端只展示、不回填搜索框。
	canonicalID, validID := scraper.CanonicalProviderID(movie.ProviderID)

	c.JSON(http.StatusOK, gin.H{
		"movie":         movie,
		"library_name":  libraryName,
		"userdata":      userData,
		"has_userdata":  hasUserData,
		"files":         files,
		"actors":        actors,
		"backdrops":     a.detailBackdrops(movie),
		"modified_at":   modifiedAt(movie.SourcePath),
		"locked_fields": lockedFields,
		"lock_data":     lockData,
		"metatube_id": gin.H{
			"saved":     movie.ProviderID,
			"canonical": canonicalID,
			"valid":     validID,
		},
	})
}

// detailBackdrops 返回详情抽屉「剧照」条的数据：下标 + 文件 tag。
// 主背景与 extrafanart 在 movieBackdropPaths 里是同一串连续下标，前端据此拼
// /emby/Items/{id}/Images/Backdrop/{index}；tag 用于图片换过之后刷新缓存。
func (a *App) detailBackdrops(movie store.Movie) []gin.H {
	paths := movieBackdropPaths(movie)
	items := make([]gin.H, 0, len(paths))
	for index, path := range paths {
		items = append(items, gin.H{"index": index, "tag": a.posterTag(path)})
	}
	return items
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return info.Size()
	}
	return 0
}

// modifiedAt 返回 .strm 的最后修改时间（RFC3339）；取不到返回空串。
func modifiedAt(path string) string {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().Format(time.RFC3339)
	}
	return ""
}
