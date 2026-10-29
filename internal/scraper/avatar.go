package scraper

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"emby-go/internal/avatar"
	"emby-go/internal/imageutil"
	"emby-go/internal/metatube"
	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

// 演员头像任务（需求 5g）：独立于影片刮削，可单独设 cron。
//
// 落盘三步：① 写 NFO 的 <actor><thumb>（远端 URL，真源）→ ② 下载转 webp 存 avatars/
// → ③ 单文件重扫刷新 DB 索引与 People[].PrimaryImageTag。
//
// 匹配口径刻意从严：头像是「人脸」，错配的观感远差于缺失，而且没有人工复核环节，
// 所以要求姓名或别名归一化后精确相等，宁可不做也不做错。

// AvatarResult 一个演员的头像处理结果。
type AvatarResult struct {
	Actor              string
	Provider           string
	AvatarURL          string
	Image              string
	Movies             int
	NFOFailed          int
	IndexRefreshFailed int
	Status             string // success / skipped / failed
	Reason             string
	// Fatal 标记这次失败是否属于环境性故障（MetaTube 不可达/鉴权失败）。
	// 与影片刮削 ItemResult.Fatal 同义，决定要不要计入熔断计数。
	Fatal bool
}

// ErrAvatarWriteIncomplete reports saved output whose NFO/index stages failed.
// It deliberately excludes underlying file paths, SQL and upstream responses.
var ErrAvatarWriteIncomplete = errors.New("头像文件已保存，但部分 NFO/索引更新未确认；请检查并修复 NFO 后重新扫库")

// RunAvatars 批量补全头像：只处理「头像为空」的演员，已存在的不重复下载（幂等）。
func (s *Scraper) RunAvatars(ctx context.Context, libraryID int64, limit int,
	onProgress func(Progress), onItem func(AvatarResult)) (Progress, error) {
	if !s.cfg.Configured() {
		return Progress{}, errors.New("未配置 MetaTube 地址或 token")
	}
	if s.store == nil {
		return Progress{}, errors.New("缺少存储，无法读取演员列表")
	}
	names, err := s.store.ActorsMissingAvatar(libraryID, limit)
	if err != nil {
		return Progress{}, err
	}
	return s.runAvatars(ctx, names, onProgress, onItem)
}

// RunAvatarsFor 只处理指定的演员名（单条路径与测试用）。
func (s *Scraper) RunAvatarsFor(ctx context.Context, names []string,
	onProgress func(Progress), onItem func(AvatarResult)) (Progress, error) {
	if !s.cfg.Configured() {
		return Progress{}, errors.New("未配置 MetaTube 地址或 token")
	}
	return s.runAvatars(ctx, names, onProgress, onItem)
}

func (s *Scraper) runAvatars(ctx context.Context, names []string,
	onProgress func(Progress), onItem func(AvatarResult)) (Progress, error) {
	progress := Progress{Total: len(names)}
	if len(names) == 0 {
		return progress, nil
	}

	var (
		mu         sync.Mutex
		aborted    atomic.Bool
		incomplete atomic.Bool
	)
	emit := func(result AvatarResult) {
		// Serialize state and both callbacks as one delivery. Workers may finish
		// out of order, but observers must never see progress move backwards or
		// receive another item's progress between this item's two callbacks.
		mu.Lock()
		defer mu.Unlock()
		if result.NFOFailed > 0 || result.IndexRefreshFailed > 0 {
			incomplete.Store(true)
		}
		progress.Done++
		progress.Current = result.Actor
		switch result.Status {
		case StatusSuccess:
			progress.Success++
			progress.Consecutive = 0
		case StatusSkipped:
			progress.Skipped++
			progress.Consecutive = 0
		default:
			progress.Failed++
			// 与影片刮削（run.go 的 emit）同一口径：只有环境性故障才累加
			// 连续失败。图片 404、下载不到这类内容性问题说明 MetaTube 有响应，
			// 应当清零——否则「一大批演员都缺图」会把整批任务误熔断掉。
			if result.Fatal {
				progress.Consecutive++
			} else {
				progress.Consecutive = 0
			}
		}
		snapshot := progress
		if onProgress != nil {
			onProgress(snapshot)
		}
		if onItem != nil {
			onItem(result)
		}
		if result.Status == StatusFailed {
			consecutive := snapshot.Consecutive
			if consecutive >= MaxConsecutiveFailures {
				if aborted.CompareAndSwap(false, true) {
					slog.Warn("头像任务触发熔断：连续失败过多，中止本次任务", "consecutive", consecutive)
				}
			}
		}
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < s.cfg.Workers(); i++ {
		wg.Go(func() {
			for name := range jobs {
				if ctx.Err() != nil || aborted.Load() {
					continue
				}
				emit(s.scrapeAvatar(ctx, name))
			}
		})
	}
	for _, name := range names {
		if ctx.Err() != nil || aborted.Load() {
			break
		}
		jobs <- name
	}
	close(jobs)
	wg.Wait()

	resultErr := ctx.Err()
	if aborted.Load() {
		resultErr = errors.Join(resultErr, fmt.Errorf("连续 %d 个演员头像失败，疑似 MetaTube 不可达或 token 失效，任务已提前中止", MaxConsecutiveFailures))
	}
	if incomplete.Load() {
		resultErr = errors.Join(resultErr, ErrAvatarWriteIncomplete)
	}
	return progress, resultErr
}

// scrapeAvatar 处理单个演员。
func (s *Scraper) scrapeAvatar(ctx context.Context, name string) AvatarResult {
	result := AvatarResult{Actor: name}
	results, err := s.client.SearchActors(ctx, name, "", true)
	if err != nil {
		return s.avatarFailure(result, "演员搜索失败: "+err.Error(), err)
	}
	match, ok := matchActor(results, name)
	if !ok {
		result.Status, result.Reason = StatusSkipped, "无姓名/别名精确匹配的演员"
		slog.Warn("头像任务跳过：无精确匹配", "actor", name, "candidates", len(results))
		return result
	}
	if len(match.Images) == 0 {
		result.Status, result.Reason = StatusSkipped, "该演员没有图片"
		return result
	}
	result.Provider, result.AvatarURL = match.Provider, strings.TrimSpace(match.Images[0])
	if result.AvatarURL == "" {
		result.Status, result.Reason = StatusSkipped, "图片地址为空"
		return result
	}

	data, err := s.client.Download(ctx, result.AvatarURL, maxImageBytes)
	if err != nil {
		return s.avatarFailure(result, "下载头像失败: "+err.Error(), err)
	}
	dir := s.avatarsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return s.avatarFailure(result, "创建头像目录失败: "+err.Error(), nil)
	}
	dest := avatar.Path(dir, name)
	if err := writeWebP(data, dest); err != nil {
		return s.avatarFailure(result, "写入头像失败: "+err.Error(), nil)
	}
	result.Image = dest

	// 写 NFO 真源 + 刷新索引（每部参演影片各一份 NFO）。
	movies, err := s.store.MoviesByActor(name)
	if err != nil {
		return s.avatarFailure(result, "查询参演影片失败: "+err.Error(), nil)
	}
	for _, movie := range movies {
		if strings.TrimSpace(movie.NFOPath) == "" {
			continue
		}
		changed, err := nfo.SetActorThumb(movie.NFOPath, name, result.AvatarURL)
		if err != nil {
			result.NFOFailed++
			slog.Warn("头像 NFO 写入未确认", "movie_id", movie.ID)
			continue
		}
		if !changed {
			continue
		}
		if err := s.rescan(movie); err != nil {
			result.IndexRefreshFailed++
			slog.Warn("头像 NFO 已保存但索引刷新未确认", "movie_id", movie.ID)
			continue
		}
		result.Movies++
	}
	// 索引里的头像标识与远端地址最后写：重扫会把 <thumb> 带进 actors.avatar_url，
	// 但重扫不认识本地副本，avatar_tag 只能由这里补。
	if err := s.store.SetIndexedActorAvatar(name, result.AvatarURL, avatar.Tag(data)); err != nil {
		return s.avatarFailure(result, "写入头像索引失败: "+err.Error(), nil)
	}
	if result.NFOFailed > 0 || result.IndexRefreshFailed > 0 {
		return s.avatarFailure(result, ErrAvatarWriteIncomplete.Error(), nil)
	}
	result.Status = StatusSuccess
	slog.Info("演员头像已更新", "actor", name, "provider", match.Provider, "movies", result.Movies)
	return result
}

// rescan 单文件重扫，刷新该片的 DB 索引（含演员与头像地址）。
func (s *Scraper) rescan(movie store.Movie) error {
	library, err := s.store.Library(movie.LibraryID)
	if err != nil {
		return err
	}
	_, err = scanner.RescanOne(s.store, library, movie.SourcePath)
	return err
}

// avatarFailure 记录一次演员处理失败。
// upstreamErr 只用于判定是否环境性故障（是否计入熔断）：本地错误（建目录、
// 写文件、写库）传 nil，别让它们被当成 MetaTube 不可达。
func (s *Scraper) avatarFailure(result AvatarResult, reason string, upstreamErr error) AvatarResult {
	result.Status, result.Reason = StatusFailed, reason
	result.Fatal = metatube.IsFatal(upstreamErr)
	slog.Warn("演员头像处理失败", "actor", result.Actor, "reason", reason, "fatal", result.Fatal)
	return result
}

// avatarsDir 头像目录；未配置时与数据库同级（需求 A4 #20）。
func (s *Scraper) avatarsDir() string {
	if dir := strings.TrimSpace(s.cfg.AvatarsDir); dir != "" {
		return dir
	}
	return "avatars"
}

// matchActor 在搜索结果里找姓名或别名与查询名精确匹配的那条。
// 归一化只做「全角转半角 + 去空白 + 小写」，不做模糊匹配——
// 头像是人脸，错配比缺失更糟（需求 5g 的匹配口径）。
func matchActor(results []metatube.ActorSearchResult, query string) (metatube.ActorSearchResult, bool) {
	want := foldName(query)
	for _, item := range results {
		if foldName(item.Name) == want {
			return item, true
		}
		for _, alias := range item.Aliases {
			if foldName(alias) == want {
				return item, true
			}
		}
	}
	return metatube.ActorSearchResult{}, false
}

// foldName 归一化演员名：全角转半角、去所有空白、转小写。
func foldName(raw string) string {
	var builder strings.Builder
	for _, char := range strings.TrimSpace(raw) {
		switch {
		case char == 0x3000: // 全角空格
			continue
		case char >= 0xFF01 && char <= 0xFF5E: // 全角 ASCII
			char -= 0xFEE0
		case unicode.IsSpace(char):
			continue
		}
		builder.WriteRune(unicode.ToLower(char))
	}
	return builder.String()
}

// writeWebP 把下载到的图片字节转 webp 落盘（临时文件 + 原子 rename）。
// 下载失败或转码失败都不会留下半截文件。
func writeWebP(data []byte, dest string) error {
	// Same directory for rename atomicity; O_EXCL and a random suffix give each
	// writer its own inode. Never reopen by path or touch the legacy dest.tmp.
	// 0666 retains os.Create's umask-filtered creation mode.
	tmp := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+"."+rand.Text()+".tmp")
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err = imageutil.EncodeWebPTo(bytes.NewReader(data), file); err != nil {
		_ = file.Close()
		return fmt.Errorf("转 webp 失败: %w", err)
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
