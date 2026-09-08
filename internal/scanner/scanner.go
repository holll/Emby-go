package scanner

import (
	"bufio"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
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

type scannedMovie struct {
	movie store.Movie
	size  int64
	mtime time.Time
}

func Scan(s *store.Store, lib store.Library) (Result, error) {
	var result Result
	paths := make(map[string]struct{})
	var imageMovies []scannedMovie
	err := filepath.Walk(lib.Path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".strm") {
			return nil
		}
		paths[path] = struct{}{}
		movie := store.Movie{LibraryID: lib.ID, SourcePath: path, OutputDir: filepath.Dir(path), Status: "pending"}
		line, err := ReadSource(path)
		if err != nil {
			result.Failed++
			return nil
		}
		setSource(&movie, line)
		var actorNames []string
		if ValidHTTP(line) {
			nfoPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".nfo"
			meta, nfoErr := nfo.Read(nfoPath)
			if nfoErr == nil {
				movie.Status = "success"
				movie.NFOPath = nfoPath
				movie.Number = meta.Number
				movie.Title = meta.Title
				movie.OriginalTitle = meta.OriginalTitle
				movie.Plot = meta.Plot
				movie.Year = meta.Year
				movie.Premiere = firstNonEmpty(meta.Premiered, meta.ReleaseDate)
				movie.Rating = meta.Rating
				movie.Director = meta.Director
				movie.Series = meta.Series
				movie.Maker = meta.Maker
				movie.Label = meta.Label
				movie.Collection = meta.Collection()
				movie.OfficialRating = meta.Mpaa
				movie.SortName = meta.SortTitle
				movie.Taglines = meta.TaglineList()
				movie.ProviderID = meta.ProviderID()
				movie.Genres = meta.Genres
				movie.Tags = meta.Tags
				movie.Studios = meta.Studios
				// 片长：NFO <runtime>（分钟）优先，缺省用 fileinfo 的秒数回退。
				movie.RuntimeSeconds = meta.RuntimeSeconds()
				for _, actor := range meta.Actors {
					actorNames = append(actorNames, actor.Name)
				}
				result.Success++
			} else {
				result.Pending++
			}
		} else {
			movie.Status = "incompatible"
			result.Incompatible++
		}
		id, err := s.UpsertMovie(movie, info.Size(), info.ModTime())
		if err != nil {
			return err
		}
		if err := s.ReplaceActors(id, actorNames); err != nil {
			return err
		}
		if movie.Status == "success" {
			imageMovies = append(imageMovies, scannedMovie{movie: movie, size: info.Size(), mtime: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for _, scanned := range imageMovies {
		var imageErr error
		// Emby 兼容：poster 可来自 poster/folder/cover/default 任一命名的源图。
		if scanned.movie.PosterPath, imageErr = imageutil.EnsurePoster(scanned.movie.OutputDir); imageErr != nil {
			return result, imageErr
		}
		if scanned.movie.BackdropPath, imageErr = imageutil.EnsureWebP(scanned.movie.OutputDir, "fanart"); imageErr != nil {
			return result, imageErr
		}
		if scanned.movie.LandscapePath, imageErr = imageutil.EnsureWebP(scanned.movie.OutputDir, "landscape"); imageErr != nil {
			return result, imageErr
		}
		if _, imageErr = s.UpsertMovie(scanned.movie, scanned.size, scanned.mtime); imageErr != nil {
			return result, imageErr
		}
	}
	if err := s.DeleteMissingSources(lib.ID, paths); err != nil {
		return result, err
	}
	return result, s.BumpVersion(lib.ID)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
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
