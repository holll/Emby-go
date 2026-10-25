package scanner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"emby-go/internal/imageutil"
	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

type Result struct {
	Success int `json:"success"`
	Pending int `json:"pending"`
	// 组级增量扫描统计。full 也会写入基线，因此这两组字段对两种模式都有效。
	Mode             string `json:"mode,omitempty"`
	GroupsTotal      int    `json:"groups_total,omitempty"`
	GroupsDone       int    `json:"groups_done,omitempty"`
	UnchangedGroups  int    `json:"unchanged_groups,omitempty"`
	Added            int    `json:"added,omitempty"`
	Updated          int    `json:"updated,omitempty"`
	Deleted          int    `json:"deleted,omitempty"`
	IndexChanged     bool   `json:"index_changed,omitempty"`
	CleanupPerformed bool   `json:"cleanup_performed"`
	Incomplete       bool   `json:"incomplete,omitempty"`
	// Skipped 本次扫描跳过的文件数（非 http(s) 源的 .strm）。
	// 这类文件不再进入 movies 表，只在扫描结果里报个数。
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// UnreadableNFO 表示侧车存在但解析失败的条目数。这类条目保留上一版元数据
	// 与可见状态，只跟随文件刷新派生列，原因写入影片的 last_scrape_error。
	UnreadableNFO int `json:"unreadable_nfo,omitempty"`
	// IndexedSources is populated only by RescanOne after metadata/actors commit.
	// It is internal evidence, not a new public scan-response field.
	IndexedSources []string `json:"-"`
}

type candidate struct {
	path     string
	info     os.FileInfo
	base     string
	part     int
	groupKey string
}

// scanOptions 一次扫描的上下文。
//
// manualPaths 是「该库中已标记为人工录入的 source_path」集合：扫库/重扫只负责按磁盘
// 现状重建索引，不该把人工录入降级成自动刮削结果，所以命中的条目保留 manual。
type scanOptions struct {
	manualPaths    map[string]bool
	indexedSources *[]string
	nonDestructive bool
}

// Progress 单次媒体库扫描的进度快照，供管理端轮询展示。
type Progress struct {
	LibraryID   int64  `json:"library_id"`
	LibraryName string `json:"library_name"`
	Total       int    `json:"total"`
	Done        int    `json:"done"`
	Current     string `json:"current"`
	Result      Result `json:"result"`
}

var cdPartPattern = regexp.MustCompile(`(?i)^(.*?)[ ._-]+CD([1-9][0-9]*)$`)

// Scan 按 Emby 的 stacking 语义处理末尾 -CDn 文件：CD1 为逻辑影片，CD2..CDn 作为 AdditionalParts。
func Scan(s *store.Store, lib store.Library) (Result, error) {
	return ScanWithProgress(s, lib, nil)
}

// ScanWithProgress 与 Scan 相同，但在每个分组处理完后回调进度（可为 nil）。
// 默认 full：旧调用者行为不变，但会记录组基线供后续 incremental 使用。
func ScanWithProgress(s *store.Store, lib store.Library, onProgress func(Progress)) (Result, error) {
	return ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: ScanModeFull}, onProgress)
}

// RescanOne 只重扫一个 .strm，用于单条刮削/编辑后立即刷新索引。
//
// 与 Scan 的两点关键差异：
//   - **不跑 DeleteMissingSources**：单文件重扫不该触发「按磁盘现状删索引」，
//     否则一次误传路径就能删掉整库索引；
//   - 只处理该文件所属的分组（CD1/CD2 同组一起重建 AdditionalParts），
//     批量场景下逐条调用它是 O(n²)，所以批量路径仍走整库 Scan。
func RescanOne(s *store.Store, lib store.Library, strmPath string) (Result, error) {
	return rescanOne(s, lib, strmPath, false)
}

// ReindexOne rebuilds current metadata without deleting invalid source records,
// including other members of a CD group. Skipped targets remain incomplete.
func ReindexOne(s *store.Store, lib store.Library, strmPath string) (Result, error) {
	return rescanOne(s, lib, strmPath, true)
}

func rescanOne(s *store.Store, lib store.Library, strmPath string, nonDestructive bool) (Result, error) {
	var result Result
	info, err := os.Stat(strmPath)
	if err != nil {
		return result, err
	}
	if info.IsDir() || !strings.EqualFold(filepath.Ext(strmPath), ".strm") {
		return result, errors.New("不是 .strm 文件: " + strmPath)
	}
	// 与整库扫描同口径：重扫不覆盖已有的 manual（详情抽屉的「重读源」也走这里）。
	manualPaths, err := s.ManualSourcePaths(lib.ID)
	if err != nil {
		return result, err
	}
	opts := scanOptions{manualPaths: manualPaths, indexedSources: &result.IndexedSources, nonDestructive: nonDestructive}

	base := strings.TrimSuffix(filepath.Base(strmPath), filepath.Ext(strmPath))
	candidate := candidate{path: strmPath, info: info, base: base, groupKey: strmPath}
	if match := cdPartPattern.FindStringSubmatch(base); match != nil {
		candidate.base = match[1]
		candidate.part = parsePart(match[2])
		candidate.groupKey = filepath.Join(filepath.Dir(strmPath), strings.ToLower(match[1]))
	}

	paths := make(map[string]struct{})
	if err := scanGroup(s, lib, collectGroup(candidate), opts, &result, paths); err != nil {
		return result, err
	}
	return finishIndexScan(result)
}

// Every mutation now commits its own version. A failed read still makes the
// inventory incomplete, so Scan skips reconciliation and reports an error.
func finishIndexScan(result Result) (Result, error) {
	if result.Failed > 0 {
		return result, errors.New("scan incomplete: source read failed")
	}
	return result, nil
}

// collectGroup 找到与目标候选同一 CD 分组的所有文件；非分集文件只返回它自己。
// 分组必须从磁盘现读：AdditionalParts 依赖同目录里实际存在的 CD2..CDn。
func collectGroup(target candidate) []candidate {
	if target.part == 0 {
		return []candidate{target}
	}
	entries, err := os.ReadDir(filepath.Dir(target.path))
	if err != nil {
		return []candidate{target}
	}
	group := []candidate{target}
	want := strings.ToLower(target.base)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		match := cdPartPattern.FindStringSubmatch(name)
		if match == nil || strings.ToLower(match[1]) != want {
			continue
		}
		path := filepath.Join(filepath.Dir(target.path), entry.Name())
		if path == target.path {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		group = append(group, candidate{path: path, info: info, base: match[1],
			part: parsePart(match[2]), groupKey: target.groupKey})
	}
	return group
}

func stackedGroup(group []candidate) (candidate, []candidate, bool) {
	var primary candidate
	var found bool
	for _, item := range group {
		if item.part == 1 {
			primary, found = item, true
			break
		}
	}
	if !found || len(group) < 2 {
		return candidate{}, nil, false
	}
	sort.Slice(group, func(i, j int) bool { return group[i].part < group[j].part })
	parts := make([]candidate, 0, len(group)-1)
	for _, item := range group {
		if item.path != primary.path {
			parts = append(parts, item)
		}
	}
	return primary, parts, true
}

// scanGroup shares CD stacking between library scans and single-file reindexing.
// Progress reporting and missing-source reconciliation remain with the caller.
func scanGroup(s *store.Store, lib store.Library, group []candidate, opts scanOptions, result *Result, paths map[string]struct{}) error {
	primary, parts, ok := stackedGroup(group)
	if !ok {
		for _, item := range group {
			if err := scanCandidate(s, lib, item, nil, opts, result, paths); err != nil {
				return err
			}
		}
		return nil
	}
	partPaths := make([]string, 0, len(parts))
	for _, part := range parts {
		partPaths = append(partPaths, part.path)
	}
	return scanCandidate(s, lib, primary, partPaths, opts, result, paths)
}

func scanCandidate(s *store.Store, lib store.Library, item candidate, additionalParts []string, opts scanOptions, result *Result, paths map[string]struct{}) error {
	paths[item.path] = struct{}{}
	movie := store.Movie{LibraryID: lib.ID, SourcePath: item.path, OutputDir: filepath.Dir(item.path), Status: "pending", AdditionalParts: additionalParts}
	line, err := ReadSource(item.path)
	if err != nil {
		result.Failed++
		// Continue indexing readable items, but report an incomplete scan at
		// the end and never run missing-source cleanup on this inventory.
		return nil
	}
	setSource(&movie, line)
	// 非 http(s) 源（ed2k 等）直接跳过：它们永远无法通过 Emby 播放，
	// 写进 movies 表只会污染列表与统计。只在扫描结果里报个数。
	if !ValidHTTP(line) {
		if opts.nonDestructive {
			result.Skipped++
			return nil
		}
		// 同一路径可能曾经是有效源，单文件重扫也必须剔除旧的可播放索引。
		if id, err := s.MovieIDByPath(item.path); err == nil {
			if err := s.DeleteIndexedMovie(id); err != nil && !store.NotFound(err) {
				return err
			}
		} else if !store.NotFound(err) {
			return err
		}
		result.Skipped++
		return nil
	}
	var actors []store.ActorRef
	// Retain legacy discovery precedence: existing registered CD1 sidecars
	// must still be read after edits even if a shared NFO also exists.
	// New stacks without a sidecar are written to the shared path.
	nfoPath := nfo.SidecarPath(item.path, false)
	meta, nfoErr := nfo.Read(nfoPath)
	// 记住「文件在但读不出来」的候选；只有回退到共享 NFO 并读成功才清掉。
	unreadablePath, unreadableErr := "", error(nil)
	if nfoErr != nil && !errors.Is(nfoErr, os.ErrNotExist) {
		unreadablePath, unreadableErr = nfoPath, nfoErr
	}
	if nfoErr != nil && len(additionalParts) > 0 {
		nfoPath = nfo.SidecarPath(item.path, true)
		meta, nfoErr = nfo.Read(nfoPath)
		switch {
		case nfoErr == nil:
			unreadablePath, unreadableErr = "", nil
		case !errors.Is(nfoErr, os.ErrNotExist):
			unreadablePath, unreadableErr = nfoPath, nfoErr
		}
	}
	if nfoErr == nil {
		applyMeta(&movie, meta, nfoPath)
		// NFO 的 <actor><thumb> 是头像真源：一并带进索引，删库重建后仍可恢复。
		for _, actor := range meta.Actors {
			actors = append(actors, store.ActorRef{Name: actor.Name, AvatarURL: strings.TrimSpace(actor.Thumb)})
		}
		// 图片与元数据同一趟写入，避免成功影片入库两次。
		movie.PosterPath = imageutil.FindPoster(movie.OutputDir)
		movie.BackdropPath = imageutil.FindImage(movie.OutputDir, "fanart")
		movie.LandscapePath = imageutil.FindImage(movie.OutputDir, "landscape")
		// 人工录入的分类要留住：扫库/重扫不覆盖已有的 manual，
		// 只有自动刮削成功（Apply 写入后由调用方显式改状态）才会变成已刮削。
		if opts.manualPaths[item.path] {
			movie.Status = "manual"
		}
	}
	if unreadablePath != "" {
		// 侧车存在但读不出来：这不是「删掉元数据」的信号。已索引的行保留旧元数据
		// 与可见状态，只跟随文件刷新派生列；原因写进 last_scrape_error。
		if err := s.IndexMovieUnreadableNFO(movie, item.info.Size(), item.info.ModTime(), nfoNote(unreadablePath, unreadableErr)); err != nil {
			return err
		}
		result.UnreadableNFO++
		return nil
	}
	id, err := s.IndexMovie(movie, item.info.Size(), item.info.ModTime(), actors)
	if err != nil {
		return err
	}
	if nfoErr == nil {
		// 读取成功：清掉上一轮留下的「NFO 不可读」备注，刮削结论不受影响。
		if err := s.ClearScanNFONote(id); err != nil {
			return err
		}
		result.Success++
		if opts.indexedSources != nil {
			*opts.indexedSources = append(*opts.indexedSources, item.path)
		}
	} else {
		result.Pending++
	}
	return nil
}

// nfoNote 描述一次「文件在但读不出来」的原因，供 last_scrape_error 展示。
func nfoNote(path string, err error) string {
	note := store.ScanNFONotePrefix + path + ": " + err.Error()
	if runes := []rune(note); len(runes) > 300 {
		note = string(runes[:300]) + "…"
	}
	return note
}

func applyMeta(movie *store.Movie, meta nfo.MovieMeta, nfoPath string) {
	movie.Status, movie.NFOPath = "success", nfoPath
	movie.Number, movie.Title, movie.OriginalTitle, movie.Plot = meta.Number, meta.Title, meta.OriginalTitle, meta.Plot
	movie.Year, movie.Premiere, movie.Rating = meta.Year, firstNonEmpty(meta.Premiered, meta.ReleaseDate), meta.Rating
	movie.Director, movie.Series, movie.Maker, movie.Label = meta.Director, meta.Series, meta.Maker, meta.Label
	movie.Collection, movie.OfficialRating, movie.SortName = meta.Collection(), meta.Mpaa, meta.SortTitle
	movie.Taglines, movie.ProviderID = meta.TaglineList(), meta.ProviderID()
	movie.Genres, movie.Tags, movie.Studios, movie.RuntimeSeconds = meta.Genres, meta.Tags, meta.Studios, meta.RuntimeSeconds()
}

func parsePart(raw string) int {
	var value int
	for _, digit := range raw {
		value = value*10 + int(digit-'0')
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func setSource(movie *store.Movie, raw string) {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme != "" {
		movie.SourceProtocol = strings.ToLower(u.Scheme)
		movie.SourceContainer = container(u.Path)
		return
	}
	if index := strings.Index(raw, "://"); index > 0 {
		movie.SourceProtocol = strings.ToLower(raw[:index])
	}
}

func container(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

func ReadSource(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadString('\n')
	if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
		err = nil
	}
	return strings.TrimSpace(line), err
}

func ValidHTTP(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// SourceStat 返回 strm 源文件的大小与修改时间；文件不可读（外部库被移动/删除）时
// 返回零值，让索引里的元数据仍可更新而不至于 panic。
func SourceStat(path string) (int64, time.Time) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, time.Time{}
	}
	return info.Size(), info.ModTime()
}
