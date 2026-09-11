package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/avatar"
	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

// 管理端单条详情：媒体墙详情抽屉的数据源。
//
// 只读，不含编辑/收藏/标记已看。技术参数复用 streamsFor()（主文件读 NFO、
// 分段读各自 mediainfo.json），演员复用 db.Actors()，不重复实现解析逻辑。

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
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	paths := append([]string{movie.SourcePath}, movie.AdditionalParts...)
	files := make([]detailFile, 0, len(paths))
	for i, path := range paths {
		streams, probed := a.detailStreams(movie, path, i == 0)
		file := detailFile{
			Index: i + 1, Path: path, Name: filepath.Base(path),
			Role: "part", Streams: streams, Probed: probed,
		}
		if i == 0 {
			file.Role = "main"
			// 主文件体积取探测写入 NFO 的 <fileinfo><size>：.strm 本身只有几十字节，
			// 对用户没有意义；探测前的回退值是 .strm 的真实大小。
			if _, size := a.nfoFileInfo(movie); size > 0 {
				file.Size = size
			} else {
				file.Size = fileSize(path)
			}
		} else {
			file.MediaInfo = mediaInfoPath(path)
			file.Size = fileSize(path)
			if raw, ok := readMediaInfoFile(path); ok {
				if info, err := infoFromMediaInfo(raw); err == nil && info.SizeBytes > 0 {
					file.Size = info.SizeBytes
				}
			}
		}
		files = append(files, file)
	}

	actors := make([]detailActor, 0, 8)
	if list, err := a.db.Actors(movie.ID); err == nil {
		dir := a.avatarsDir()
		for _, actor := range list {
			item := detailActor{Name: actor.Name, AvatarURL: actor.AvatarURL}
			if path := avatar.Path(dir, actor.Name); fileExists(path) {
				item.HasImage = true
				item.ImageTag = actor.AvatarTag
				if item.ImageTag == "" {
					item.ImageTag = a.posterTag(path)
				}
			}
			actors = append(actors, item)
		}
	}

	libraryName := ""
	if lib, err := a.db.Library(movie.LibraryID); err == nil {
		libraryName = lib.Name
	}
	data, _ := a.db.DataFor([]int64{movie.ID})
	userData, hasUserData := data[movie.ID]

	c.JSON(http.StatusOK, gin.H{
		"movie":        movie,
		"library_name": libraryName,
		"userdata":     userData,
		"has_userdata": hasUserData,
		"files":        files,
		"actors":       actors,
		"modified_at":  modifiedAt(movie.SourcePath),
	})
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return info.Size()
	}
	return 0
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// modifiedAt 返回 .strm 的最后修改时间（RFC3339）；取不到返回空串。
func modifiedAt(path string) string {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().Format(time.RFC3339)
	}
	return ""
}

// detailStreams 返回某个媒体文件的流信息与「是否已探测」。
//
// 主文件的「已探测」以 NFO 是否有 <streamdetails> 为准；分段以是否存在自己的
// mediainfo.json 为准——分段没有缓存时 streamsFor 会回退主文件 NFO，
// 那不是分段的探测结果，必须报未探测，否则页面会把主文件参数标成分段的。
func (a *App) detailStreams(movie store.Movie, path string, primary bool) ([]gin.H, bool) {
	if !primary {
		file, ok := readMediaInfoFile(path)
		if !ok {
			return nil, false
		}
		info, err := infoFromMediaInfo(file)
		if err != nil {
			return nil, false
		}
		streams := buildStreams(streamDetailsFromProbe(info))
		return streams, len(streams) > 0
	}
	streams, _ := a.streamsFor(movie, path)
	return streams, a.nfoProbed(movie)
}

// nfoProbed 判断影片主文件是否已被探测（NFO 里有 <streamdetails>）。
func (a *App) nfoProbed(movie store.Movie) bool {
	if movie.NFOPath == "" {
		return false
	}
	meta, err := nfo.Read(movie.NFOPath)
	if err != nil || meta.FileInfo == nil || meta.FileInfo.StreamDetails == nil {
		return false
	}
	details := meta.FileInfo.StreamDetails
	return details.Video != nil || details.Audio != nil
}
