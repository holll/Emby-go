package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"emby-go/internal/collage"
	"emby-go/internal/store"
)

// 合集封面复刻 Emby 历史 Skia StripCollageBuilder.BuildThumbCollage 的四列倒影拼贴。
// 生成参数写进 collection_covers.algorithm：算法或几何一变就换标识，
// 免得把旧算法的产物当成当前算法的缓存继续用。
const (
	collectionCoverAlgorithm  = "emby-strip-4col-v1"
	collectionCoverQuality    = 90 // 源码 BuildThumbCollage 的编码质量
	collectionCoverWidth      = 640
	collectionCoverHeight     = 360
	collectionCoverColumns    = 4
	collectionCoverMaxSources = 8 // 只有 4 个槽位，留出损坏图的补位余量
)

// errCoverRenderFailed 是 leader 异常退出时给 follower 的兜底结果。
var errCoverRenderFailed = errors.New("collection cover render failed")

// collectionCoverOptions 是一次生成的参数。零值字段回落到 Emby 历史常量。
type collectionCoverOptions struct {
	Width      int
	Height     int
	Quality    int
	Reflection bool
	// Apply 为假时只落预览文件、不写库：默认不修改任何已生效的封面。
	Apply bool
	// Overwrite 为真才允许覆盖非本算法写入的既有记录（自定义上传的封面）。
	Overwrite bool
}

func (o collectionCoverOptions) normalize() collectionCoverOptions {
	if o.Width <= 0 {
		o.Width = collectionCoverWidth
	}
	if o.Height <= 0 {
		o.Height = collectionCoverHeight
	}
	if o.Quality <= 0 {
		o.Quality = collectionCoverQuality
	}
	return o
}

// collectionCoverResult 是单个合集的处理结果，也是批量任务里 Current 的展示内容。
type collectionCoverResult struct {
	Collection string `json:"collection"`
	Path       string `json:"path,omitempty"`
	SourceHash string `json:"source_hash,omitempty"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Applied    bool   `json:"applied"`
	Skipped    bool   `json:"skipped"`
	Reason     string `json:"reason,omitempty"`
}

// coverFlight 是一次「同 key 生成中」的登记：leader 生成，follower 等结果。
type coverFlight struct {
	done chan struct{}
	path string
	err  error
}

// collectionCoverDir 返回合集封面目录：与数据库文件同级的 collection-covers/。
func (a *App) collectionCoverDir() string {
	dir := filepath.Dir(a.cfg.DBPath)
	if dir == "" || dir == "." {
		return "collection-covers"
	}
	return filepath.Join(dir, "collection-covers")
}

func (a *App) collectionCoverPath(key string) string {
	return filepath.Join(a.collectionCoverDir(), key+".webp")
}

// collectionCoverSources 取参与拼贴的成员海报。
//
// 顺序用 LatestVisible 的 ORDER BY id DESC（最新入库优先）：这是本项目的兼容
// 选择，不是已证实的 Emby 上游排序。只有 4 个槽位，多取的候选只用于在解码
// 失败时补位。
func (a *App) collectionCoverSources(collection string) (ids []int64, paths []string, err error) {
	movies, err := a.db.LatestVisible(0, nil, collection, collectionCoverMaxSources, 0)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range movies {
		if m.PosterPath == "" {
			continue
		}
		ids = append(ids, m.ID)
		paths = append(paths, m.PosterPath)
	}
	return ids, paths, nil
}

// collectionCoverKey 是缓存的唯一依据：算法、几何、画质、倒影开关、合集名，
// 加上每个成员的 id 与海报 mtime tag。成员变化、海报文件被替换、参数调整
// 都会换 key，因此命中即等价于「没有变化」。
func (a *App) collectionCoverKey(collection string, ids []int64, paths []string, opts collectionCoverOptions) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%dx%d|q%d|r%t|%s", collectionCoverAlgorithm, opts.Width, opts.Height, opts.Quality, opts.Reflection, collection)
	for i, path := range paths {
		fmt.Fprintf(h, "|%d:%s", ids[i], a.posterTag(path))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// collectionCoverVariant 返回该 key 的封面文件路径：已存在即命中，否则同 key
// 只生成一次并原子落盘。
func (a *App) collectionCoverVariant(ctx context.Context, key string, paths []string, opts collectionCoverOptions) (path string, err error) {
	dest := a.collectionCoverPath(key)
	if _, statErr := os.Stat(dest); statErr == nil {
		return dest, nil
	}
	flight, leader := a.beginCoverFlight(key)
	if !leader {
		select {
		case <-flight.done:
			return flight.path, flight.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	// leader 若 panic（解码器/编码器 bug），登记不注销的话该 key 的 follower
	// 只能等到各自 ctx 超时才返回，这张封面在进程生命周期内反复重算。
	// defer 保证注销一定发生，再把 panic 原样抛出保留 monitor 的记录。
	defer func() {
		if r := recover(); r != nil {
			a.finishCoverFlight(key, flight, "", errCoverRenderFailed)
			panic(r)
		}
	}()
	path, err = a.renderCollectionCover(key, paths, opts)
	a.finishCoverFlight(key, flight, path, err)
	return path, err
}

// renderCollectionCover 是 leader 的生成路径：渲染 → 编码 → 同目录临时文件 + rename。
func (a *App) renderCollectionCover(key string, paths []string, opts collectionCoverOptions) (string, error) {
	data, err := collage.RenderWebP(paths, collage.Options{
		Layout:     collage.Layout{Width: opts.Width, Height: opts.Height, Columns: collectionCoverColumns},
		Reflection: opts.Reflection,
	}, opts.Quality)
	if err != nil {
		return "", err
	}
	dest := a.collectionCoverPath(key)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(dest, func(tmpPath string) error {
		return os.WriteFile(tmpPath, data, 0o644)
	}, nil); err != nil {
		return "", err
	}
	return dest, nil
}

func (a *App) beginCoverFlight(key string) (*coverFlight, bool) {
	a.coverMu.Lock()
	defer a.coverMu.Unlock()
	if flight, ok := a.coverFlights[key]; ok {
		return flight, false
	}
	flight := &coverFlight{done: make(chan struct{})}
	a.coverFlights[key] = flight
	return flight, true
}

// finishCoverFlight 发布结果并注销登记。先写字段再 close(done)：
// follower 收到关闭信号时一定能读到完整结果。
func (a *App) finishCoverFlight(key string, flight *coverFlight, path string, err error) {
	a.coverMu.Lock()
	flight.path, flight.err = path, err
	delete(a.coverFlights, key)
	a.coverMu.Unlock()
	close(flight.done)
}

// generateCollectionCover 生成一个合集的封面，按需写入生效记录。
func (a *App) generateCollectionCover(ctx context.Context, collection string, opts collectionCoverOptions) (collectionCoverResult, error) {
	opts = opts.normalize()
	result := collectionCoverResult{Collection: collection, Width: opts.Width, Height: opts.Height}
	ids, paths, err := a.collectionCoverSources(collection)
	if err != nil {
		return result, err
	}
	if len(paths) == 0 {
		result.Skipped, result.Reason = true, "no posters"
		return result, nil
	}
	key := a.collectionCoverKey(collection, ids, paths, opts)
	result.SourceHash = key
	path, err := a.collectionCoverVariant(ctx, key, paths, opts)
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
	// 库里已有非本算法写入的封面（例如用户上传的自定义封面）时默认不覆盖。
	previous, applied, err := a.db.AppliedCover(collection)
	if err != nil {
		return result, err
	}
	if applied && previous.Algorithm != collectionCoverAlgorithm && !opts.Overwrite {
		result.Skipped, result.Reason = true, "custom cover kept"
		return result, nil
	}
	err = a.db.SetAppliedCover(store.CollectionCover{
		Collection: collection,
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
	// 同一路径被重新生成时 mtime 变了，tag 缓存必须失效，否则客户端继续拿旧图。
	a.invalidateImageTag(path)
	a.invalidateAppliedCovers()
	result.Applied = true
	return result, nil
}

// appliedCoversCacheKey 是「全部生效封面」的进程内缓存键。
//
// 合集列表逐条渲染 DTO，每个合集各查一次库会在单连接 SQLite 上排队；这张表
// 只有合集封面端点会写，apply/revert 后显式失效，30 秒 TTL 兜住其它进程的写入。
const appliedCoversCacheKey = "collection-covers:applied"

// appliedCovers 返回全部生效封面。查询失败返回空 map：列表照常出，封面退回代表海报。
func (a *App) appliedCovers() map[string]store.CollectionCover {
	if raw, ok := a.imgMeta.Get(appliedCoversCacheKey); ok {
		var covers map[string]store.CollectionCover
		if json.Unmarshal(raw, &covers) == nil {
			return covers
		}
	}
	covers, err := a.db.AppliedCovers()
	if err != nil {
		return map[string]store.CollectionCover{}
	}
	if raw, err := json.Marshal(covers); err == nil {
		a.imgMeta.Set(appliedCoversCacheKey, raw, 30*time.Second)
	}
	return covers
}

// appliedCover 返回某个合集的生效封面路径。记录存在但文件已不在时当作未生效，
// 让合集回退到代表海报，而不是给客户端一个必然 404 的图。
func (a *App) appliedCover(collection string) (string, bool) {
	cover, ok := a.appliedCovers()[collection]
	if !ok {
		return "", false
	}
	if _, err := os.Stat(cover.Path); err != nil {
		return "", false
	}
	return cover.Path, true
}

func (a *App) invalidateAppliedCovers() {
	a.imgMeta.Delete(appliedCoversCacheKey)
	// 合集文件夹（boxsets 视图）的封面另有 5 分钟缓存，键里只有全局版本号，
	// 换封面不会改版本号——不显式删掉的话客户端最长 5 分钟看不到新封面。
	a.imgMeta.Delete("boxcover:" + a.db.Version("g:version"))
}

// firstCollectionCover 返回第一个有已生效封面的合集路径（没有则空串）。
func (a *App) firstCollectionCover(names []string) string {
	covers := a.appliedCovers()
	for _, name := range names {
		if cover, ok := covers[name]; ok {
			if _, err := os.Stat(cover.Path); err == nil {
				return cover.Path
			}
		}
	}
	return ""
}
