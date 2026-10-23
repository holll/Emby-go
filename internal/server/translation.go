package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/scheduler"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

const maxTranslationIDs = 1000
const maxTranslationLimit = 100000
const translationBatchSize = 100

var errTranslationBusy = errors.New("翻译或其他 NFO 写入任务正在进行中")
var errTranslationTarget = errors.New("translation target not found or not visible")
var errTranslationDisabled = errors.New("尚未配置翻译服务")
var errTranslationRefresh = errors.New("翻译已保存，但索引刷新失败；请修复存储后重读源，不要重复翻译")

type translationRequest struct {
	MovieIDs  []int64  `json:"movie_ids,omitempty"`
	LibraryID int64    `json:"library_id,omitempty"`
	Fields    []string `json:"fields"`
	Limit     int      `json:"limit,omitempty"`
}

type translationStatus struct {
	scrapeStatus
	TaskID int64 `json:"task_id"`
}

type translationState struct {
	mu     sync.RWMutex
	status translationStatus
	cancel context.CancelFunc
	// Optional deterministic item seam; assigned before a job starts, never mutated
	// while workers run. Production always uses scraper.TranslateExisting.
	item func(context.Context, store.Movie, scraper.TranslationOptions) (scraper.TranslationResult, error)
}

// decodeTranslationRequest is shared by manual requests and scheduled params.
// Preserve presence: []/null movie_ids must not silently become an all-library job.
func decodeTranslationRequest(data []byte, scheduled bool) (translationRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return translationRequest{}, errors.New("expected a JSON object")
	}
	req := translationRequest{Fields: []string{"title"}}
	for key, value := range raw {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && (key == "fields" || key == "movie_ids" || key == "library_id" || key == "limit") {
			return req, fmt.Errorf("%s cannot be null", key)
		}
		switch key {
		case "movie_ids":
			if scheduled {
				return req, errors.New("scheduled translation does not support movie_ids")
			}
			if err := json.Unmarshal(value, &req.MovieIDs); err != nil || len(req.MovieIDs) == 0 || len(req.MovieIDs) > maxTranslationIDs {
				return req, fmt.Errorf("movie_ids must contain 1..%d positive IDs", maxTranslationIDs)
			}
			seen := map[int64]bool{}
			ids := make([]int64, 0, len(req.MovieIDs))
			for _, id := range req.MovieIDs {
				if id <= 0 {
					return req, errors.New("movie_ids must be positive")
				}
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			req.MovieIDs = ids
		case "library_id":
			if err := json.Unmarshal(value, &req.LibraryID); err != nil || req.LibraryID < 0 {
				return req, errors.New("library_id must be nonnegative")
			}
		case "limit":
			if err := json.Unmarshal(value, &req.Limit); err != nil || req.Limit < 0 || req.Limit > maxTranslationLimit {
				return req, fmt.Errorf("limit must be 0..%d", maxTranslationLimit)
			}
		case "fields":
			if err := json.Unmarshal(value, &req.Fields); err != nil || len(req.Fields) == 0 {
				return req, errors.New("fields must select title and/or plot")
			}
			title, plot := false, false
			for _, field := range req.Fields {
				switch field {
				case "title":
					title = true
				case "plot":
					plot = true
				default:
					return req, errors.New("fields must select title and/or plot")
				}
			}
			req.Fields = nil
			if title {
				req.Fields = append(req.Fields, "title")
			}
			if plot {
				req.Fields = append(req.Fields, "plot")
			}
		default:
			if !scheduled {
				return req, fmt.Errorf("unknown translation field %q", key)
			}
		}
	}
	if _, libraryPresent := raw["library_id"]; len(req.MovieIDs) > 0 && libraryPresent {
		return req, errors.New("movie_ids and library_id are mutually exclusive")
	}
	return req, nil
}

func normalizeTranslationParams(raw map[string]any) (map[string]any, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		data = []byte("{}")
	}
	req, err := decodeTranslationRequest(data, true)
	if err != nil {
		return nil, err
	}
	// Keep opaque/hidden parameters intact for conditional editor round trips.
	out := make(map[string]any, len(raw)+1)
	for k, v := range raw {
		out[k] = v
	}
	out["fields"] = req.Fields
	if req.LibraryID > 0 {
		out["library_id"] = req.LibraryID
	}
	if _, present := raw["limit"]; present {
		out["limit"] = req.Limit
	}
	return out, nil
}

func (a *App) prepareTranslation(req translationRequest) (scraper.Config, int, error) {
	cfg, err := a.scrapeConfigChecked()
	if err != nil {
		return cfg, 0, fmt.Errorf("translation configuration unavailable: %w", err)
	}
	if !cfg.Translate.Enabled() {
		return cfg, 0, errTranslationDisabled
	}
	if len(req.MovieIDs) > 0 {
		// Validate every explicit ID, even if limit would later truncate the list.
		for _, id := range req.MovieIDs {
			m, err := a.db.Movie(id)
			if store.NotFound(err) || (err == nil && !m.IsVisible()) {
				return cfg, 0, errTranslationTarget
			}
			if err != nil {
				return cfg, 0, err
			}
		}
		total := len(req.MovieIDs)
		if req.Limit > 0 && total > req.Limit {
			total = req.Limit
		}
		return cfg, total, nil
	}
	if req.LibraryID > 0 {
		if _, err := a.db.Library(req.LibraryID); err != nil {
			if store.NotFound(err) {
				return cfg, 0, errTranslationTarget
			}
			return cfg, 0, err
		}
	}
	total := 0
	err = a.walkTranslation(a.rootCtx, req, func(store.Movie) error { total++; return nil })
	return cfg, total, err
}

// walkTranslation uses a bounded ID cursor; non-visible rows never count toward
// limit. Explicit IDs are re-read and fail closed rather than broadening scope.
func (a *App) walkTranslation(ctx context.Context, req translationRequest, visit func(store.Movie) error) error {
	done := 0
	accept := func(m store.Movie) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(m); err != nil {
			return err
		}
		done++
		return nil
	}
	if len(req.MovieIDs) > 0 {
		for _, id := range req.MovieIDs {
			if req.Limit > 0 && done >= req.Limit {
				break
			}
			m, err := a.db.Movie(id)
			if store.NotFound(err) || (err == nil && !m.IsVisible()) {
				return errTranslationTarget
			}
			if err != nil {
				return err
			}
			if err := accept(m); err != nil {
				return err
			}
		}
		return nil
	}
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		movies, err := a.db.MoviesForScrapeBatch(req.LibraryID, false, after, translationBatchSize)
		if err != nil {
			return err
		}
		if len(movies) == 0 {
			return nil
		}
		for _, m := range movies {
			after = m.ID
			if !m.IsVisible() {
				continue
			}
			if req.Limit > 0 && done >= req.Limit {
				return nil
			}
			if err := accept(m); err != nil {
				return err
			}
		}
	}
}

// beginTranslation claims both gates before the handler can return 202. No
// goroutine waits for the shared NFO lane and scheduled/manual starts share it.
func (a *App) beginTranslation(total int) (context.Context, int64, error) {
	a.translation.mu.Lock()
	defer a.translation.mu.Unlock()
	if err := a.rootCtx.Err(); err != nil {
		return nil, 0, err
	}
	if a.translation.status.Running || !a.claimNFO("translate") {
		return nil, 0, errTranslationBusy
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	taskID := a.startTask("translate")
	a.translation.cancel = cancel
	a.translation.status = translationStatus{TaskID: taskID, scrapeStatus: scrapeStatus{
		Running: true, Kind: "translate", Total: total, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}}
	return ctx, taskID, nil
}

func (a *App) translationSnapshot() translationStatus {
	a.translation.mu.RLock()
	defer a.translation.mu.RUnlock()
	status := a.translation.status
	status.Failures = append([]string(nil), status.Failures...)
	return status
}

func (a *App) executeTranslation(ctx context.Context, taskID int64, cfg scraper.Config, req translationRequest) (err error) {
	defer func() {
		a.cache.Clear()
		a.translation.mu.Lock()
		status := &a.translation.status
		status.Running = false
		status.Current = ""
		status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		status.Cancelled = errors.Is(ctx.Err(), context.Canceled)
		if status.IndexRefreshFailed {
			err = errors.Join(err, errTranslationRefresh)
		}
		// A joined cancellation must not hide an already-recorded item failure,
		// especially committed NFO output whose index refresh failed.
		if err != nil && (status.Failed > 0 || !errors.Is(err, context.Canceled)) {
			status.Error = err.Error()
		}
		if a.translation.cancel != nil {
			a.translation.cancel()
		}
		a.translation.cancel = nil
		a.releaseNFO("translate")
		a.translation.mu.Unlock()
		a.finishTask(taskID, err)
	}()
	engine := scraper.New(cfg, a.db)
	item := engine.TranslateExisting
	if a.translation.item != nil {
		item = a.translation.item
	}
	opts := scraper.TranslationOptions{Refresh: true}
	for _, field := range req.Fields {
		if field == "title" {
			opts.Title = true
		}
		if field == "plot" {
			opts.Plot = true
		}
	}
	itemFailed := false
	walkErr := a.walkTranslation(ctx, req, func(m store.Movie) error {
		a.translation.mu.Lock()
		a.translation.status.Current = m.Title
		a.translation.mu.Unlock()
		result, itemErr := item(ctx, m, opts)
		if ctx.Err() != nil && errors.Is(itemErr, ctx.Err()) {
			return itemErr // an interrupted item is not a completed/failed translation
		}
		a.translation.mu.Lock()
		status := &a.translation.status
		status.Done++
		if status.Done > status.Total {
			status.Total = status.Done
		}
		switch {
		case itemErr != nil || result.RefreshError != "":
			status.Failed++
			itemFailed = true
			reason := result.RefreshError
			if itemErr != nil {
				reason = itemErr.Error()
			}
			if len(status.Failures) < maxScrapeFailureSamples {
				status.Failures = append(status.Failures, fmt.Sprintf("%d: %s", m.ID, reason))
			}
			if result.RefreshError != "" {
				status.IndexRefreshFailed = true
			}
		case result.Skipped || !result.Changed:
			status.Skipped++
		default:
			status.Success++
		}
		a.translation.mu.Unlock()
		return nil
	})
	if itemFailed {
		err = errors.New("部分影片翻译失败，请检查失败样例")
	}
	return errors.Join(err, walkErr)
}

func (a *App) adminTranslationRun(c *gin.Context) {
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		apiError(c, http.StatusBadRequest, "invalid json")
		return
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	req, err := decodeTranslationRequest(data, false)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	cfg, total, err := a.prepareTranslation(req)
	if err != nil {
		translationAPIError(c, err)
		return
	}
	ctx, taskID, err := a.beginTranslation(total)
	if err != nil {
		translationAPIError(c, err)
		return
	}
	// Capture the accepted generation, not a later task's snapshot.
	accepted := a.translationSnapshot()
	a.workerWG.Add(1)
	go func() { defer a.workerWG.Done(); _ = a.executeTranslation(ctx, taskID, cfg, req) }()
	c.JSON(http.StatusAccepted, accepted)
}

func translationAPIError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errTranslationBusy):
		apiError(c, http.StatusConflict, err.Error())
	case errors.Is(err, errTranslationDisabled):
		apiError(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, errTranslationTarget):
		apiError(c, http.StatusNotFound, err.Error())
	default:
		apiError(c, http.StatusInternalServerError, "translation service unavailable")
	}
}

func (a *App) adminTranslationStatus(c *gin.Context) {
	status := a.translationSnapshot()
	if value, exists := c.GetQuery("task_id"); exists {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			apiError(c, http.StatusBadRequest, "invalid task_id")
			return
		}
		if id != status.TaskID {
			apiError(c, http.StatusNotFound, "translation task not found")
			return
		}
	}
	c.JSON(http.StatusOK, status)
}

func (a *App) adminTranslationCancel(c *gin.Context) {
	var req struct {
		TaskID *int64 `json:"task_id"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		apiError(c, http.StatusBadRequest, "invalid cancel request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		apiError(c, http.StatusBadRequest, "invalid cancel request")
		return
	}
	if req.TaskID != nil && *req.TaskID <= 0 {
		apiError(c, http.StatusBadRequest, "invalid task_id")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	a.translation.mu.Lock()
	defer a.translation.mu.Unlock()
	if !a.translation.status.Running || a.translation.cancel == nil || (req.TaskID != nil && *req.TaskID != a.translation.status.TaskID) {
		apiError(c, http.StatusConflict, "当前翻译任务不存在、已结束或已变化")
		return
	}
	a.translation.cancel()
	c.JSON(http.StatusOK, gin.H{"status": "cancelling", "task_id": a.translation.status.TaskID})
}

func (a *App) runScheduledTranslation(task scheduler.Task) error {
	data, err := json.Marshal(task.Params)
	if err != nil {
		return err
	}
	if task.Params == nil {
		data = []byte("{}")
	}
	req, err := decodeTranslationRequest(data, true)
	if err != nil {
		return err
	}
	cfg, total, err := a.prepareTranslation(req)
	if err != nil {
		return err
	}
	ctx, taskID, err := a.beginTranslation(total)
	if errors.Is(err, errTranslationBusy) {
		return scheduler.ErrBusy
	}
	if err != nil {
		return err
	}
	return a.executeTranslation(ctx, taskID, cfg, req)
}
