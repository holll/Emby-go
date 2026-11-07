package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

const subtitleMarksImageNote = "未重下载海报，旧图角标可能未同步"

type subtitleMarksRequest struct {
	Overwrite bool
	Version   string
}

// Both adapters validate untrusted JSON once; internal operations use concrete values.
func decodeSubtitleMarks(body io.Reader, confirm bool) (subtitleMarksRequest, error) {
	var req subtitleMarksRequest
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&raw); err != nil {
		return req, err
	}
	if raw == nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return req, errors.New("one JSON object required")
	}
	for key, value := range raw {
		switch key {
		case "overwrite":
			var v *bool
			if json.Unmarshal(value, &v) != nil || v == nil {
				return req, errors.New("overwrite must be boolean")
			}
			req.Overwrite = *v
		case "version":
			if !confirm || json.Unmarshal(value, &req.Version) != nil {
				return req, errors.New("version only accepted by confirmation")
			}
		default:
			return req, errors.New("unknown subtitle mark field")
		}
	}
	if confirm {
		digest, err := hex.DecodeString(req.Version)
		if err != nil || len(digest) != sha256.Size {
			return req, errors.New("preview version required")
		}
	}
	return req, nil
}

type subtitleMarksSnapshot struct {
	Movie    store.Movie
	Library  store.Library
	Path     string
	Evidence scraper.SubtitleMarkEvidence
	Current  []string
	Plan     nfo.AutoMarksPlan
	Version  string
}

// Shared by preview and confirm; confirmation calls this only after claiming the
// exclusive NFO channel, so previous ownership is never the pre-gate DB snapshot.
func (a *App) readSubtitleMarks(c *gin.Context, id int64, overwrite bool) (subtitleMarksSnapshot, bool) {
	var snapshot subtitleMarksSnapshot
	movie, err := a.db.Movie(id)
	if err != nil {
		status := http.StatusServiceUnavailable
		if store.NotFound(err) {
			status = http.StatusNotFound
		}
		apiError(c, status, "影片索引暂不可用")
		return snapshot, false
	}
	lib, err := a.db.Library(movie.LibraryID)
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, "媒体库暂不可用")
		return snapshot, false
	}
	source, err := scanner.ReadSource(movie.SourcePath)
	if err != nil || !scanner.ValidHTTP(source) || !strings.EqualFold(filepath.Ext(movie.SourcePath), ".strm") {
		apiError(c, http.StatusUnprocessableEntity, "主片 STRM 不可读或无有效播放地址，未执行整理")
		return snapshot, false
	}
	evidence, err := scraper.DetectSubtitleMark(movie.SourcePath)
	if err != nil {
		apiError(c, http.StatusUnprocessableEntity, "字幕目录不可读，不能据此撤销标签")
		return snapshot, false
	}
	current := []string{}
	if evidence.Tag != "" {
		current = append(current, evidence.Tag)
	}
	path := editNFOPath(movie)
	plan, err := nfo.PreviewAutoMarks(path, movie.AutoMarks, current, overwrite)
	if err != nil {
		apiError(c, http.StatusUnprocessableEntity, "现有 NFO 不可读或损坏，未执行整理")
		return snapshot, false
	}
	// Every field is a concrete string, integer, boolean or slice thereof; Marshal
	// cannot fail. No timestamp/cache version is included: unrelated edits do not
	// expire an otherwise identical local preview.
	encoded, _ := json.Marshal(struct {
		MovieID, LibraryID                  int64
		Title, LibraryName, LibraryPath     string
		SourcePath, Source, NFOPath, Digest string
		Parts, Previous                     []string
		Evidence                            scraper.SubtitleMarkEvidence
		Overwrite                           bool
	}{movie.ID, lib.ID, movie.Title, lib.Name, lib.Path, movie.SourcePath, source, path, plan.Digest,
		movie.AdditionalParts, movie.AutoMarks, evidence, overwrite})
	sum := sha256.Sum256(encoded)
	return subtitleMarksSnapshot{movie, lib, path, evidence, current, plan, hex.EncodeToString(sum[:])}, true
}

func (a *App) adminSubtitleMarksPreview(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	req, err := decodeSubtitleMarks(http.MaxBytesReader(c.Writer, c.Request.Body, 4096), false)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	defer release()
	snapshot, ok := a.readSubtitleMarks(c, id, req.Overwrite)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"movie_id": id, "title": snapshot.Movie.Title, "library_name": snapshot.Library.Name,
		"main_file": filepath.Base(snapshot.Movie.SourcePath), "version": snapshot.Version,
		"overwrite": req.Overwrite, "evidence": snapshot.Evidence,
		"before": snapshot.Plan.Before, "after": snapshot.Plan.After, "owned": snapshot.Plan.Owned,
		"previous_owned": append([]string{}, snapshot.Movie.AutoMarks...),
		"protected":      snapshot.Plan.Protected, "changed": snapshot.Plan.Changed || !slices.Equal(snapshot.Movie.AutoMarks, snapshot.Plan.Owned),
		"image_note": subtitleMarksImageNote,
	})
}

func (a *App) adminSubtitleMarksConfirm(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	req, err := decodeSubtitleMarks(http.MaxBytesReader(c.Writer, c.Request.Body, 4096), true)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := a.requestContext(c)
	defer cancel()
	if !a.admitAdminOperationContext(c, ctx) {
		return
	}
	if !a.claimNFO("subtitle_marks") {
		apiError(c, http.StatusConflict, a.nfoBusyOwner()+"正在进行中，请稍后重新预览")
		return
	}
	defer a.releaseNFO("subtitle_marks")
	snapshot, ok := a.readSubtitleMarks(c, id, req.Overwrite)
	if !ok {
		return
	}
	if snapshot.Version != req.Version {
		apiError(c, http.StatusConflict, "整理依据已变化，请重新预览后确认；未写入")
		return
	}
	if ctx.Err() != nil {
		apiError(c, http.StatusServiceUnavailable, "整理在写入前已取消；未写入")
		return
	}
	plan, err := nfo.CommitAutoMarks(snapshot.Path, snapshot.Movie.AutoMarks, snapshot.Current, req.Overwrite, snapshot.Plan.Digest)
	if err != nil {
		if errors.Is(err, nfo.ErrAutoMarksConflict) {
			apiError(c, http.StatusConflict, "NFO 已变化，请重新预览后确认；未写入")
		} else {
			apiError(c, http.StatusUnprocessableEntity, "NFO 标签写入失败，未更新归属或索引")
		}
		return
	}
	response := gin.H{"status": "success", "saved": true, "indexed": false, "ownership_saved": true, "warnings": []string{subtitleMarksImageNote}}
	ownershipChanged := !slices.Equal(snapshot.Movie.AutoMarks, plan.Owned)
	// Once NFO has committed, persist actual ownership before observing cancel.
	// A failure here is not recoverable by ordinary indexing or a blind resubmit.
	if ownershipChanged {
		if err := a.db.SetAutoMarks(id, plan.Owned); err != nil {
			slog.Warn("字幕标记归属保存失败", "movie_id", id, "error", err)
			response["status"], response["ownership_saved"] = "partial_success", false
			response["auto_marks_error"] = "NFO 已确认保存，但归属记录失败；请人工核对，不要直接重新整理；重扫不能恢复归属。"
		}
	}
	a.cache.Clear()
	if ctx.Err() != nil {
		response["status"], response["canceled"] = "partial_success", true
		response["refresh_error"] = "文件已确认保存，取消后未刷新索引；请重新扫描该影片。"
		c.JSON(http.StatusOK, response)
		return
	}
	if !plan.Changed && !ownershipChanged && subtitleMarkTagsEqual(snapshot.Movie.Tags, plan.After) {
		response["status"], response["indexed"] = "unchanged", true
		c.JSON(http.StatusOK, response)
		return
	}
	if err := a.indexSubtitleMarks(snapshot, plan.After); err != nil {
		slog.Warn("字幕标记索引刷新失败", "movie_id", id, "error", err)
		response["status"] = "partial_success"
		response["refresh_error"] = "文件已确认保存，但索引刷新未完成；请重新扫描该影片或媒体库。"
	} else {
		response["indexed"] = true
	}
	c.JSON(http.StatusOK, response)
}

// Organizer plans trim tag edges, while the ordinary index preserves raw XML
// values. Compare the plan's semantics without rewriting protected human tags.
func subtitleMarkTagsEqual(indexed, planned []string) bool {
	return slices.EqualFunc(indexed, planned, func(a, b string) bool {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	})
}

// Unlike manual edit recovery this never calls MarkIndexedManual or changes the
// scrape result. ReindexOne is the existing non-destructive, one-group executor.
func (a *App) indexSubtitleMarks(snapshot subtitleMarksSnapshot, tags []string) error {
	result, err := scanner.ReindexOne(a.db, snapshot.Library, snapshot.Movie.SourcePath)
	if err != nil {
		return err
	}
	if result.Failed != 0 || result.Pending != 0 || result.Skipped != 0 || result.Incomplete || !slices.Contains(result.IndexedSources, snapshot.Movie.SourcePath) {
		return fmt.Errorf("subtitle marks index incomplete")
	}
	movie, err := a.db.Movie(snapshot.Movie.ID)
	if err != nil {
		return err
	}
	if filepath.Clean(movie.NFOPath) != filepath.Clean(snapshot.Path) || !subtitleMarkTagsEqual(movie.Tags, tags) {
		return fmt.Errorf("subtitle marks index does not match saved NFO")
	}
	return nil
}
