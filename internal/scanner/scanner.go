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

type scannedMovie struct {
	movie store.Movie
	size  int64
	mtime time.Time
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
	var successful []scannedMovie
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
				if err := scanCandidate(s, lib, item, nil, "", &result, paths, &successful); err != nil {
					return result, err
				}
			}
		} else {
			partPaths := make([]string, 0, len(parts))
			for _, part := range parts {
				partPaths = append(partPaths, part.path)
			}
			fallbackNFO := filepath.Join(filepath.Dir(primary.path), primary.base+".nfo")
			if err := scanCandidate(s, lib, primary, partPaths, fallbackNFO, &result, paths, &successful); err != nil {
				return result, err
			}
		}
		done++
		report(done, key)
	}

	for _, scanned := range successful {
		scanned.movie.PosterPath = imageutil.FindPoster(scanned.movie.OutputDir)
		scanned.movie.BackdropPath = imageutil.FindImage(scanned.movie.OutputDir, "fanart")
		scanned.movie.LandscapePath = imageutil.FindImage(scanned.movie.OutputDir, "landscape")
		if _, err := s.UpsertMovie(scanned.movie, scanned.size, scanned.mtime); err != nil {
			return result, err
		}
	}
	if err := s.DeleteMissingSources(lib.ID, paths); err != nil {
		return result, err
	}
	return result, s.BumpVersion(lib.ID)
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

func scanCandidate(s *store.Store, lib store.Library, item candidate, additionalParts []string, fallbackNFO string, result *Result, paths map[string]struct{}, successful *[]scannedMovie) error {
	paths[item.path] = struct{}{}
	movie := store.Movie{LibraryID: lib.ID, SourcePath: item.path, OutputDir: filepath.Dir(item.path), Status: "pending", AdditionalParts: additionalParts}
	line, err := ReadSource(item.path)
	if err != nil {
		result.Failed++
		return nil
	}
	setSource(&movie, line)
	var actors []string
	if ValidHTTP(line) {
		nfoPath := strings.TrimSuffix(item.path, filepath.Ext(item.path)) + ".nfo"
		meta, nfoErr := nfo.Read(nfoPath)
		if nfoErr != nil && fallbackNFO != "" {
			nfoPath = fallbackNFO
			meta, nfoErr = nfo.Read(fallbackNFO)
		}
		if nfoErr == nil {
			applyMeta(&movie, meta, nfoPath)
			for _, actor := range meta.Actors {
				actors = append(actors, actor.Name)
			}
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
	if err := s.ReplaceActors(id, actors); err != nil {
		return err
	}
	if movie.Status == "success" {
		*successful = append(*successful, scannedMovie{movie: movie, size: item.info.Size(), mtime: item.info.ModTime()})
	}
	return nil
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

func MTime(path string) time.Time {
	value, _ := os.Stat(path)
	if value == nil {
		return time.Time{}
	}
	return value.ModTime()
}
