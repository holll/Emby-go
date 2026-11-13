package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"emby-go/internal/collage"
	"emby-go/internal/imageutil"
	"emby-go/internal/store"
)

// 媒体库封面用同一套 StripCollageBuilder.BuildThumbCollage 拼贴，只是尺寸取
// Emby/Jellyfin 给媒体库文件夹用的 960×540（BaseDynamicImageProvider 里带
// width/height 的重载就是为此存在）。
const (
	libraryCoverWidth      = 960
	libraryCoverHeight     = 540
	libraryCoverMaxSources = 8 // 只有 4 个槽位，留出损坏图的补位余量
)

// libraryCoverSources 取参与拼贴的库内成员图，顺序同合集（最新入库优先）。
func (a *App) libraryCoverSources(libraryID int64) (ids []int64, paths []string, err error) {
	movies, err := a.db.LatestVisible(libraryID, nil, "", libraryCoverMaxSources, 0)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range movies {
		path := coverSourcePath(m)
		if path == "" {
			continue
		}
		ids = append(ids, m.ID)
		paths = append(paths, path)
	}
	return ids, paths, nil
}

// generateLibraryCover 生成一个媒体库的封面，按需写入生效记录。
func (a *App) generateLibraryCover(ctx context.Context, libraryID int64, opts coverOptions) (coverResult, error) {
	opts = opts.normalize(libraryCoverWidth, libraryCoverHeight)
	library, err := a.db.Library(libraryID)
	if err != nil {
		return coverResult{}, err
	}
	result := coverResult{Name: library.Name, Width: opts.Width, Height: opts.Height}
	ids, paths, err := a.libraryCoverSources(libraryID)
	if err != nil {
		return result, err
	}
	if len(paths) == 0 {
		result.Skipped, result.Reason = true, "no posters"
		return result, nil
	}
	layout := collage.Layout{Width: opts.Width, Height: opts.Height, Columns: collectionCoverColumns}
	key := a.coverKey("library:"+strconv.FormatInt(libraryID, 10), ids, paths, layout, opts.Quality, opts.Reflection)
	result.SourceHash = key
	path, err := a.coverVariant(ctx, a.libraryCoverDir(), key, paths, layout, opts.Quality, opts.Reflection)
	if errors.Is(err, collage.ErrNoImages) {
		result.Skipped, result.Reason = true, "no decodable posters"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Path = path
	if !opts.Apply {
		return result, nil
	}
	// 库根目录里已有管理员放的海报/上传的封面时，生成结果不覆盖它：
	// 那是明确的自定义封面，优先级高于自动拼贴。
	if uploadedLibraryCover(library.Path) != "" || imageutil.FindPoster(library.Path) != "" {
		result.Skipped, result.Reason = true, "custom cover kept"
		return result, nil
	}
	previous, applied, err := a.db.AppliedLibraryCover(libraryID)
	if err != nil {
		return result, err
	}
	if applied && previous.SourceHash == key {
		result.Applied, result.Reason = true, "up to date"
		return result, nil
	}
	err = a.db.SetAppliedLibraryCover(store.LibraryCover{
		LibraryID:  libraryID,
		Path:       path,
		SourceHash: key,
		Algorithm:  collectionCoverAlgorithm,
		Width:      opts.Width,
		Height:     opts.Height,
		AppliedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return result, err
	}
	a.invalidateImageTag(path)
	a.invalidateAppliedLibraryCovers()
	result.Applied = true
	return result, nil
}

// appliedLibraryCoversCacheKey 是「全部生效库封面」的进程内缓存键。
//
// 媒体库列表逐条渲染 DTO，每个库各查一次库会在单连接 SQLite 上排队；这张表
// 只有库封面端点会写，apply/revert 后显式失效，30 秒 TTL 兜住其它进程的写入。
const appliedLibraryCoversCacheKey = "library-covers:applied"

// appliedLibraryCovers 返回全部生效库封面。查询失败返回空 map：列表照常出，
// 封面退回库根目录海报或代表图。
func (a *App) appliedLibraryCovers() map[int64]store.LibraryCover {
	if raw, ok := a.imgMeta.Get(appliedLibraryCoversCacheKey); ok {
		var covers map[int64]store.LibraryCover
		if json.Unmarshal(raw, &covers) == nil {
			return covers
		}
	}
	covers, err := a.db.AppliedLibraryCovers()
	if err != nil {
		return map[int64]store.LibraryCover{}
	}
	if raw, err := json.Marshal(covers); err == nil {
		a.imgMeta.Set(appliedLibraryCoversCacheKey, raw, 30*time.Second)
	}
	return covers
}

// appliedLibraryCoverFile 返回某个库生效封面文件；记录存在但文件已不在时当作
// 未生效，让库回退到根目录海报或代表图，而不是给客户端一个必然 404 的图。
func appliedLibraryCoverFile(cover store.LibraryCover) string {
	if cover.Path == "" {
		return ""
	}
	if _, err := os.Stat(cover.Path); err != nil {
		return ""
	}
	return cover.Path
}

func (a *App) invalidateAppliedLibraryCovers() {
	a.imgMeta.Delete(appliedLibraryCoversCacheKey)
}
