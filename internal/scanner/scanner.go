package scanner

import (
	"bufio"
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
	Success      int `json:"success"`
	Pending      int `json:"pending"`
	Incompatible int `json:"incompatible"`
	Failed       int `json:"failed"`
}

type candidate struct {
	path     string
	info     os.FileInfo
	base     string
	part     int
	groupKey string
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
func ScanWithProgress(s *store.Store, lib store.Library, onProgress func(Progress)) (Result, error) {
	var result Result
	groups := make(map[string][]candidate)
	err := filepath.Walk(lib.Path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			// 单个目录不可读时跳过该目录，不因此中止整库重建（否则失效索引永远清不掉）。
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".strm") {
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		candidate := candidate{path: path, info: info, base: base}
		if match := cdPartPattern.FindStringSubmatch(base); match != nil {
			candidate.base = match[1]
			candidate.part = parsePart(match[2])
			candidate.groupKey = filepath.Join(filepath.Dir(path), strings.ToLower(match[1]))
		} else {
			candidate.groupKey = path
		}
		groups[candidate.groupKey] = append(groups[candidate.groupKey], candidate)
		return nil
	})
	if err != nil {
		return result, err
	}

	total := len(groups)
	report := func(done int, current string) {
		if onProgress != nil {
			onProgress(Progress{LibraryID: lib.ID, LibraryName: lib.Name, Total: total, Done: done, Current: current, Result: result})
		}
	}
	report(0, "")

	paths := make(map[string]struct{})
	groupKeys := make([]string, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)
	done := 0
	for _, key := range groupKeys {
		group := groups[key]
		primary, parts, ok := stackedGroup(group)
		if !ok {
			for _, item := range group {
				if err := scanCandidate(s, lib, item, nil, "", &result, paths); err != nil {
					return result, err
				}
			}
		} else {
			partPaths := make([]string, 0, len(parts))
			for _, part := range parts {
				partPaths = append(partPaths, part.path)
			}
			fallbackNFO := filepath.Join(filepath.Dir(primary.path), primary.base+".nfo")
			if err := scanCandidate(s, lib, primary, partPaths, fallbackNFO, &result, paths); err != nil {
				return result, err
			}
		}
		done++
		report(done, key)
	}

	if err := s.DeleteMissingSources(lib.ID, paths); err != nil {
		return result, err
	}
	return result, s.BumpVersion(lib.ID)
}

// RescanOne 只重扫一个 .strm，用于单条刮削/编辑后立即刷新索引。
//
// 与 Scan 的两点关键差异：
//   - **不跑 DeleteMissingSources**：单文件重扫不该触发「按磁盘现状删索引」，
//     否则一次误传路径就能删掉整库索引；
//   - 只处理该文件所属的分组（CD1/CD2 同组一起重建 AdditionalParts），
//     批量场景下逐条调用它是 O(n²)，所以批量路径仍走整库 Scan。
func RescanOne(s *store.Store, lib store.Library, strmPath string) (Result, error) {
	var result Result
	info, err := os.Stat(strmPath)
	if err != nil {
		return result, err
	}
	if info.IsDir() || !strings.EqualFold(filepath.Ext(strmPath), ".strm") {
		return result, errors.New("不是 .strm 文件: " + strmPath)
	}

	base := strings.TrimSuffix(filepath.Base(strmPath), filepath.Ext(strmPath))
	candidate := candidate{path: strmPath, info: info, base: base, groupKey: strmPath}
	if match := cdPartPattern.FindStringSubmatch(base); match != nil {
		candidate.base = match[1]
		candidate.part = parsePart(match[2])
		candidate.groupKey = filepath.Join(filepath.Dir(strmPath), strings.ToLower(match[1]))
	}

	paths := make(map[string]struct{})
	group := collectGroup(candidate)
	if primary, parts, ok := stackedGroup(group); ok {
		partPaths := make([]string, 0, len(parts))
		for _, part := range parts {
			partPaths = append(partPaths, part.path)
		}
		fallbackNFO := filepath.Join(filepath.Dir(primary.path), primary.base+".nfo")
		if err := scanCandidate(s, lib, primary, partPaths, fallbackNFO, &result, paths); err != nil {
			return result, err
		}
	} else {
		for _, item := range group {
			if err := scanCandidate(s, lib, item, nil, "", &result, paths); err != nil {
				return result, err
			}
		}
	}
	return result, s.BumpVersion(lib.ID)
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

func scanCandidate(s *store.Store, lib store.Library, item candidate, additionalParts []string, fallbackNFO string, result *Result, paths map[string]struct{}) error {
	paths[item.path] = struct{}{}
	movie := store.Movie{LibraryID: lib.ID, SourcePath: item.path, OutputDir: filepath.Dir(item.path), Status: "pending", AdditionalParts: additionalParts}
	line, err := ReadSource(item.path)
	if err != nil {
		result.Failed++
		return nil
	}
	setSource(&movie, line)
	var actors []store.ActorRef
	if ValidHTTP(line) {
		nfoPath := strings.TrimSuffix(item.path, filepath.Ext(item.path)) + ".nfo"
		meta, nfoErr := nfo.Read(nfoPath)
		if nfoErr != nil && fallbackNFO != "" {
			nfoPath = fallbackNFO
			meta, nfoErr = nfo.Read(fallbackNFO)
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
			result.Success++
		} else {
			result.Pending++
		}
	} else {
		movie.Status = "incompatible"
		result.Incompatible++
	}
	id, err := s.UpsertMovie(movie, item.info.Size(), item.info.ModTime())
	if err != nil {
		return err
	}
	return s.ReplaceActors(id, actors)
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
