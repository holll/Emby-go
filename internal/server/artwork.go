package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"emby-go/internal/artwork"
	"emby-go/internal/metatube"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

type artworkRequest struct {
	Mode            string   `json:"mode"`
	MovieIDs        []int64  `json:"movie_ids"`
	LibraryID       int64    `json:"library_id"`
	Limit           int      `json:"limit"`
	Provider        string   `json:"provider"`
	ID              string   `json:"id"`
	Overwrite       bool     `json:"overwrite"`
	PositionSeconds *float64 `json:"position_seconds"`
	PositionPercent *float64 `json:"position_percent"`
}

type artworkStatus struct {
	InstanceID         string  `json:"instance_id"`
	TaskID             int64   `json:"task_id"`
	Mode               string  `json:"mode"`
	MovieIDs           []int64 `json:"movie_ids"`
	Running            bool    `json:"running"`
	Total              int     `json:"total"`
	Done               int     `json:"done"`
	Success            int     `json:"success"`
	Skipped            int     `json:"skipped"`
	Failed             int     `json:"failed"`
	IndexRefreshFailed int     `json:"index_refresh_failed"`
	Current            string  `json:"current"`
	FinishedAt         string  `json:"finished_at,omitempty"`
	Error              string  `json:"error,omitempty"`
	Cancelled          bool    `json:"cancelled"`
}

type artworkState struct {
	mu       sync.RWMutex
	status   artworkStatus
	cancel   context.CancelFunc
	run      func(context.Context, *store.Store, artwork.Request, artwork.Options) (artwork.Result, error)
	lookPath func(string) (string, error)
}

// artworkObject bounds both run and cancel bodies and rejects ambiguous duplicate
// fields rather than letting the JSON decoder silently choose the last value.
func artworkObject(r io.Reader) ([]byte, map[string]json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(r, 65537))
	if err != nil || len(data) > 65536 {
		return nil, nil, errors.New("invalid request")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, nil, errors.New("expected JSON object")
	}
	raw := make(map[string]json.RawMessage)
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, nil, errors.New("invalid artwork fields")
		}
		if _, exists := raw[key]; exists {
			return nil, nil, errors.New("duplicate artwork field")
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, nil, errors.New("invalid or null artwork field")
		}
		raw[key] = value
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return nil, nil, errors.New("invalid request")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, nil, errors.New("invalid request")
	}
	return data, raw, nil
}

func decodeArtworkRequest(r io.Reader) (artworkRequest, error) {
	var req artworkRequest
	data, raw, err := artworkObject(r)
	if err != nil {
		return req, err
	}
	for key := range raw {
		switch key {
		case "mode", "movie_ids", "library_id", "limit", "provider", "id", "overwrite", "position_seconds", "position_percent":
		default:
			return req, errors.New("invalid artwork fields")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		return req, errors.New("invalid artwork fields")
	}
	if req.Mode != "screenshot" && req.Mode != "provider" {
		return req, errors.New("invalid artwork mode")
	}
	_, ids := raw["movie_ids"]
	_, lib := raw["library_id"]
	_, limit := raw["limit"]
	if ids {
		if len(req.MovieIDs) == 0 || len(req.MovieIDs) > 100 || lib || limit {
			return req, errors.New("movie_ids requires 1..100 IDs without library_id/limit")
		}
		seen := map[int64]bool{}
		for _, id := range req.MovieIDs {
			if id <= 0 || seen[id] {
				return req, errors.New("IDs must be distinct and positive")
			}
			seen[id] = true
		}
	} else if !lib || req.LibraryID <= 0 || !limit || req.Limit <= 0 || req.Limit > 100 {
		return req, errors.New("explicit library_id and limit 1..100 required")
	}
	if req.PositionSeconds != nil && req.PositionPercent != nil {
		return req, errors.New("choose one position")
	}
	for _, p := range []*float64{req.PositionSeconds, req.PositionPercent} {
		if p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0) {
			return req, errors.New("invalid position")
		}
	}
	if req.PositionSeconds != nil && *req.PositionSeconds > float64(math.MaxInt64)/float64(time.Second) {
		return req, errors.New("position_seconds exceeds supported range")
	}
	if req.PositionPercent != nil && *req.PositionPercent >= 100 {
		return req, errors.New("position_percent must be >=0 and <100")
	}
	if req.Mode == "provider" {
		if len(req.MovieIDs) != 1 || !artworkToken(req.Provider) || !artworkToken(req.ID) || req.PositionSeconds != nil || req.PositionPercent != nil {
			return req, errors.New("provider requires one movie ID, provider and id without position")
		}
	} else {
		if _, ok := raw["provider"]; ok {
			return req, errors.New("provider is only valid in provider mode")
		}
		if _, ok := raw["id"]; ok {
			return req, errors.New("id is only valid in provider mode")
		}
	}
	return req, nil
}

func artworkToken(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 256 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("/\\?&#%", r) {
			return false
		}
	}
	return true
}

func (a *App) artworkOptions(mode string) (artwork.Options, error) {
	opts := artwork.Options{Timeout: 90 * time.Second}
	if mode == "screenshot" {
		lookup := a.artwork.lookPath
		if lookup == nil {
			lookup = exec.LookPath
		}
		var err error
		opts.FFmpegPath, err = lookup("ffmpeg")
		if err != nil {
			return opts, errors.New("ffmpeg 不可用")
		}
		probe := a.cfg.FFProbePath
		if probe == "" {
			probe = "ffprobe"
		}
		opts.FFprobePath, err = lookup(probe)
		if err != nil {
			return opts, errors.New("ffprobe 不可用")
		}
	} else {
		cfg, err := a.scrapeConfigChecked()
		if err != nil || !cfg.Configured() {
			return opts, errors.New("MetaTube 配置不可用")
		}
		opts.Client = metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout())
	}
	return opts, nil
}

func (a *App) adminArtworkCapabilities(c *gin.Context) {
	if !a.admitAdminOperation(c) {
		return
	}
	lookup := a.artwork.lookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	_, fe := lookup("ffmpeg")
	probe := a.cfg.FFProbePath
	if probe == "" {
		probe = "ffprobe"
	}
	_, pe := lookup(probe)
	_, se := a.artworkOptions("screenshot")
	_, ce := a.artworkOptions("provider")
	text := func(err error) string {
		if err != nil {
			return err.Error()
		}
		return ""
	}
	c.JSON(200, gin.H{"screenshot": se == nil, "provider": ce == nil, "ffmpeg": fe == nil, "ffprobe": pe == nil, "screenshot_error": text(se), "provider_error": text(ce)})
}

func (a *App) adminArtworkRun(c *gin.Context) {
	req, err := decodeArtworkRequest(c.Request.Body)
	if err != nil {
		apiError(c, 400, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	opts, err := a.artworkOptions(req.Mode)
	if err != nil {
		apiError(c, 503, err.Error())
		return
	}
	ids := req.MovieIDs
	if len(ids) > 0 {
		for _, id := range ids {
			if _, err = a.db.Movie(id); err != nil {
				if store.NotFound(err) {
					apiError(c, 404, "artwork target not found")
				} else {
					apiError(c, 503, "artwork targets unavailable")
				}
				return
			}
		}
	} else {
		if _, err = a.db.Library(req.LibraryID); err != nil {
			if store.NotFound(err) {
				apiError(c, 404, "artwork library not found")
			} else {
				apiError(c, 503, "artwork targets unavailable")
			}
			return
		}
		movies, e := a.db.MoviesForScrape(req.LibraryID, false, req.Limit)
		if e != nil {
			apiError(c, 503, "artwork targets unavailable")
			return
		}
		for _, m := range movies {
			ids = append(ids, m.ID)
		}
	}
	a.artwork.mu.Lock()
	if a.rootCtx.Err() != nil {
		a.artwork.mu.Unlock()
		apiError(c, 503, "server shutting down")
		return
	}
	if a.artwork.status.Running || !a.claimNFO("artwork") {
		a.artwork.mu.Unlock()
		apiError(c, 409, "图片或其他 NFO 写入任务正在进行中")
		return
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	taskID := a.startTask("artwork_" + req.Mode)
	accepted := artworkStatus{InstanceID: a.instanceID, TaskID: taskID, Mode: req.Mode, MovieIDs: append([]int64{}, ids...), Running: true, Total: len(ids)}
	a.artwork.status = accepted
	a.artwork.cancel = cancel
	a.workerWG.Add(1)
	a.artwork.mu.Unlock()
	go func() { defer a.workerWG.Done(); a.executeArtwork(ctx, taskID, req, ids, opts) }()
	c.JSON(202, accepted)
}

func (a *App) executeArtwork(ctx context.Context, taskID int64, req artworkRequest, ids []int64, opts artwork.Options) {
	defer func() {
		a.artwork.mu.Lock()
		s := &a.artwork.status
		s.Running = false
		s.Current = ""
		s.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		s.Cancelled = ctx.Err() != nil
		var taskErr error
		if s.IndexRefreshFailed > 0 {
			s.Error = "图片已保存，但索引刷新失败；请重读源后检查"
		} else if s.Cancelled {
			s.Error = "任务已取消"
		} else if s.Failed > 0 {
			s.Error = "部分图片操作失败"
		}
		if s.Error != "" {
			taskErr = errors.New(s.Error)
		}
		if a.artwork.cancel != nil {
			a.artwork.cancel()
		}
		a.artwork.cancel = nil
		a.finishTask(taskID, taskErr)
		a.releaseNFO("artwork")
		a.artwork.mu.Unlock()
	}()
	run := a.artwork.run
	if run == nil {
		run = artwork.Run
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		a.artwork.mu.Lock()
		a.artwork.status.Current = strconv.FormatInt(id, 10)
		a.artwork.mu.Unlock()
		before, _ := a.db.Movie(id)
		result, err := run(ctx, a.db, artwork.Request{MovieID: id, Mode: req.Mode, Provider: req.Provider, ProviderID: req.ID, Overwrite: req.Overwrite, PositionSeconds: req.PositionSeconds, PositionPercent: req.PositionPercent}, opts)
		if result.Changed {
			// The image-only transaction advances the versioned DTO namespaces.
			// Do not flush unrelated caches (including credentials) for one poster.
			a.invalidateImageTag(before.PosterPath, result.PosterPath)
			if result.RefreshError != "" {
				// A failed post-install transaction cannot pretend the index was
				// updated. Still retire stale image-bearing DTO namespaces when
				// possible, and always evict this movie's current metadata entry.
				_ = a.db.BumpVersion(before.LibraryID)
				a.imgMeta.Delete("m:" + a.db.Version("g:version") + ":" + strconv.FormatInt(id, 10))
			}
		}
		a.artwork.mu.Lock()
		s := &a.artwork.status
		s.Done++
		switch {
		case result.RefreshError != "":
			s.IndexRefreshFailed++
			s.Failed++
		case err != nil:
			s.Failed++
		case result.Skipped:
			s.Skipped++
		case result.Changed:
			s.Success++
		default:
			s.Skipped++
		}
		a.artwork.mu.Unlock()
	}
}

func (a *App) adminArtworkStatus(c *gin.Context) {
	if len(c.Request.URL.Query()["task_id"]) > 1 {
		apiError(c, 400, "invalid task_id")
		return
	}
	var id int64
	if value, specified := c.GetQuery("task_id"); specified {
		var err error
		id, err = strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			apiError(c, 400, "invalid task_id")
			return
		}
	} else if _, present := c.Request.URL.Query()["task_id"]; present {
		apiError(c, 400, "invalid task_id")
		return
	}
	a.artwork.mu.RLock()
	s := a.artwork.status
	a.artwork.mu.RUnlock()
	instance := c.Query("instance_id")
	if len(c.Request.URL.Query()["instance_id"]) > 1 || (id != 0 && instance == "") {
		apiError(c, 400, "instance_id required for task lookup")
		return
	}
	if id != 0 && (s.TaskID != id || instance != a.instanceID) {
		apiError(c, 404, "artwork task not found")
		return
	}
	s.InstanceID = a.instanceID
	// Without an ID, return the latest task for reconnecting admin clients.
	// Target IDs let the UI avoid refreshing or cancelling another movie's task.
	c.JSON(200, s)
}

func (a *App) adminArtworkCancel(c *gin.Context) {
	var req struct {
		TaskID     int64  `json:"task_id"`
		InstanceID string `json:"instance_id"`
	}
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil || len(raw) != 2 || raw["task_id"] == nil || raw["instance_id"] == nil {
		apiError(c, 400, "invalid cancel request")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || req.TaskID <= 0 || req.InstanceID == "" || len(req.InstanceID) > 256 {
		apiError(c, 400, "invalid task_id")
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		apiError(c, 400, "invalid cancel request")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	a.artwork.mu.Lock()
	defer a.artwork.mu.Unlock()
	if a.artwork.status.TaskID != req.TaskID || req.InstanceID != a.instanceID {
		apiError(c, 404, "artwork task not found")
		return
	}
	if !a.artwork.status.Running || a.artwork.cancel == nil {
		apiError(c, 409, "artwork task finished")
		return
	}
	a.artwork.cancel()
	c.JSON(200, gin.H{"instance_id": a.instanceID, "task_id": req.TaskID, "status": "cancelling"})
}

func (a *App) adminArtworkCandidates(c *gin.Context) {
	id, err := strconv.ParseInt(c.Query("movie_id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(c, 400, "invalid movie_id")
		return
	}
	provider, q := c.Query("provider"), strings.TrimSpace(c.Query("q"))
	if (provider != "" && !artworkToken(provider)) || len(q) > 256 {
		apiError(c, 400, "invalid search parameters")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	movie, err := a.db.Movie(id)
	if err != nil {
		if store.NotFound(err) {
			apiError(c, 404, "artwork target not found")
		} else {
			apiError(c, 503, "artwork target unavailable")
		}
		return
	}
	if q == "" {
		q = strings.TrimSpace(movie.Number)
		if q == "" {
			q = strings.TrimSpace(movie.Title)
		}
	}
	if q == "" || len(q) > 256 || strings.ContainsAny(q, "\r\n") || strings.Contains(strings.ToLower(q), "http:") || strings.Contains(strings.ToLower(q), "https:") || strings.Contains(q, "//") || strings.IndexFunc(q, unicode.IsControl) >= 0 {
		apiError(c, 400, "search title or number required; URLs are not accepted")
		return
	}
	opts, err := a.artworkOptions("provider")
	if err != nil {
		apiError(c, 503, err.Error())
		return
	}
	ctx, cancel := a.requestContext(c)
	defer cancel()
	results, err := opts.Client.SearchMovies(ctx, q, provider, true)
	if err != nil {
		apiError(c, 502, "MetaTube 搜索失败")
		return
	}
	candidates := make([]gin.H, 0)
	for _, r := range results {
		if len(candidates) >= 100 {
			break
		}
		if !artworkToken(r.Provider) || !artworkToken(r.ID) {
			continue
		}
		preview := func(kind string) string {
			return "/api/admin/scrape/image?" + url.Values{"kind": {kind}, "provider": {r.Provider}, "id": {r.ID}}.Encode()
		}
		candidates = append(candidates, gin.H{"provider": r.Provider, "id": r.ID, "number": artworkText(r.Number), "title": artworkText(r.Title), "score": r.Score, "release_date": artworkText(r.ReleaseDate), "poster": preview("primary"), "thumb": preview("thumb")})
	}
	c.JSON(200, gin.H{"candidates": candidates})
}

func artworkText(s string) string {
	r := []rune(s)
	if len(r) > 256 {
		r = r[:256]
	}
	return string(r)
}
