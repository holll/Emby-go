package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// coverTaskStatus 是封面批量任务的可轮询状态，字段与 artworkStatus 同风格。
// 合集任务填 collections、媒体库任务填 libraries，kind 说明是哪一类。
type coverTaskStatus struct {
	InstanceID  string   `json:"instance_id"`
	TaskID      int64    `json:"task_id"`
	Kind        string   `json:"kind"`
	Mode        string   `json:"mode"`
	Collections []string `json:"collections,omitempty"`
	Libraries   []int64  `json:"libraries,omitempty"`
	Running     bool     `json:"running"`
	Total       int      `json:"total"`
	Done        int      `json:"done"`
	Success     int      `json:"success"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	Current     string   `json:"current"`
	Cancelled   bool     `json:"cancelled"`
	Error       string   `json:"error,omitempty"`
	FinishedAt  string   `json:"finished_at,omitempty"`
}

// collectionCoverRequest 是 run 端点的请求体。
//
// quality/width/height 用指针区分「没给」与「给了 0」：前者回落 Emby 历史常量，
// 后者是非法值，必须报错而不是被静默兜底。
type collectionCoverRequest struct {
	Collections []string `json:"collections"`
	All         bool     `json:"all"`
	Apply       bool     `json:"apply"`
	Overwrite   bool     `json:"overwrite"`
	Quality     *int     `json:"quality"`
	Width       *int     `json:"width"`
	Height      *int     `json:"height"`
	Reflection  *bool    `json:"reflection"`
}

// collectionCoverMaxBatch 是一次批量任务的合集上限：只为挡住误传的超大列表。
const collectionCoverMaxBatch = 200

func decodeCollectionCoverRequest(r io.Reader) (collectionCoverRequest, error) {
	var req collectionCoverRequest
	data, raw, err := artworkObject(r)
	if err != nil {
		return req, err
	}
	for key := range raw {
		switch key {
		case "collections", "all", "apply", "overwrite", "quality", "width", "height", "reflection":
		default:
			return req, errors.New("invalid collection cover fields")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		return req, errors.New("invalid collection cover fields")
	}
	if req.All == (len(req.Collections) > 0) {
		return req, errors.New("provide either collections or all")
	}
	if len(req.Collections) > collectionCoverMaxBatch {
		return req, errors.New("too many collections")
	}
	for i, name := range req.Collections {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 256 {
			return req, errors.New("invalid collection name")
		}
		req.Collections[i] = name
	}
	if req.Quality != nil && (*req.Quality < 1 || *req.Quality > 100) {
		return req, errors.New("quality must be 1..100")
	}
	for _, edge := range []*int{req.Width, req.Height} {
		if edge != nil && (*edge < 64 || *edge > 4096) {
			return req, errors.New("width and height must be 64..4096")
		}
	}
	return req, nil
}

// coverOptionsFrom 把已校验的请求字段折成生成参数：没给的回落 Emby 历史常量。
// 合集与媒体库两种请求体共用这一处映射。
func coverOptionsFrom(quality, width, height *int, reflection *bool, overwrite bool, mode string) coverOptions {
	opts := coverOptions{Reflection: true, Apply: mode == "apply", Overwrite: overwrite}
	if quality != nil {
		opts.Quality = *quality
	}
	if width != nil {
		opts.Width = *width
	}
	if height != nil {
		opts.Height = *height
	}
	if reflection != nil {
		opts.Reflection = *reflection
	}
	return opts
}

func (req collectionCoverRequest) options(mode string) coverOptions {
	return coverOptionsFrom(req.Quality, req.Width, req.Height, req.Reflection, req.Overwrite, mode)
}

func (a *App) adminCollectionCoverRun(c *gin.Context) {
	req, err := decodeCollectionCoverRequest(c.Request.Body)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	names := req.Collections
	if req.All {
		names = a.cachedCollections()
	}
	mode := "preview"
	if req.Apply {
		mode = "apply"
	}
	accepted, err := a.startCoverTask(collectionTargets(names), req.options(mode), mode)
	if err != nil {
		workerGateAPIError(c, err, http.StatusConflict)
		return
	}
	c.JSON(http.StatusAccepted, accepted)
}

func (a *App) executeCoverTargets(ctx context.Context, taskID int64, targets []coverTarget, opts coverOptions) {
	defer func() {
		a.coverTaskMu.Lock()
		s := &a.coverStatus
		s.Running = false
		s.Current = ""
		s.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		s.Cancelled = ctx.Err() != nil
		var taskErr error
		switch {
		case s.Cancelled:
			s.Error = "任务已取消"
		case s.Failed > 0:
			s.Error = "部分封面生成失败"
		}
		if s.Error != "" {
			taskErr = errors.New(s.Error)
		}
		if a.coverCancel != nil {
			a.coverCancel()
		}
		a.coverCancel = nil
		a.finishTask(taskID, taskErr)
		a.coverTaskMu.Unlock()
	}()
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		a.coverTaskMu.Lock()
		a.coverStatus.Current = target.Name
		a.coverTaskMu.Unlock()
		var result coverResult
		var err error
		if target.Kind == coverTargetLibrary {
			result, err = a.generateLibraryCover(ctx, target.ID, opts)
		} else {
			result, err = a.generateCollectionCover(ctx, target.Name, opts)
		}
		a.coverTaskMu.Lock()
		s := &a.coverStatus
		s.Done++
		switch {
		case err != nil:
			s.Failed++
		case result.Skipped:
			s.Skipped++
		default:
			s.Success++
		}
		a.coverTaskMu.Unlock()
	}
}

func (a *App) adminCollectionCoverStatus(c *gin.Context) {
	if len(c.Request.URL.Query()["task_id"]) > 1 {
		apiError(c, http.StatusBadRequest, "invalid task_id")
		return
	}
	var id int64
	if value, specified := c.GetQuery("task_id"); specified {
		var err error
		id, err = strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			apiError(c, http.StatusBadRequest, "invalid task_id")
			return
		}
	}
	a.coverTaskMu.RLock()
	s := a.coverStatus
	a.coverTaskMu.RUnlock()
	instance := c.Query("instance_id")
	if len(c.Request.URL.Query()["instance_id"]) > 1 || (id != 0 && instance == "") {
		apiError(c, http.StatusBadRequest, "instance_id required for task lookup")
		return
	}
	if id != 0 && (s.TaskID != id || instance != a.instanceID) {
		apiError(c, http.StatusNotFound, "cover task not found")
		return
	}
	s.InstanceID = a.instanceID
	c.JSON(http.StatusOK, s)
}

func (a *App) adminCollectionCoverCancel(c *gin.Context) {
	var req struct {
		TaskID     int64  `json:"task_id"`
		InstanceID string `json:"instance_id"`
	}
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil || len(raw) != 2 || raw["task_id"] == nil || raw["instance_id"] == nil {
		apiError(c, http.StatusBadRequest, "invalid cancel request")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || req.TaskID <= 0 || req.InstanceID == "" || len(req.InstanceID) > 256 {
		apiError(c, http.StatusBadRequest, "invalid task_id")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	a.coverTaskMu.Lock()
	defer a.coverTaskMu.Unlock()
	if a.coverStatus.TaskID != req.TaskID || req.InstanceID != a.instanceID {
		apiError(c, http.StatusNotFound, "cover task not found")
		return
	}
	if !a.coverStatus.Running || a.coverCancel == nil {
		apiError(c, http.StatusConflict, "cover task finished")
		return
	}
	a.coverCancel()
	c.JSON(http.StatusOK, gin.H{"instance_id": a.instanceID, "task_id": req.TaskID, "status": "cancelling"})
}

// adminCollectionCoverPreview 当场生成（或复用缓存）一个合集的封面并回传字节，
// 供管理页 <img> 直接引用。它不写库：预览与生效是两件事。
func (a *App) adminCollectionCoverPreview(c *gin.Context) {
	name := strings.TrimSpace(c.Query("collection"))
	if name == "" || len(name) > 256 {
		apiError(c, http.StatusBadRequest, "invalid collection")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	// 只有达到合集阈值的合集才有封面，与列表/详情保持同一判定。
	stats, ok := a.collectionStats(c, 0)
	if !ok {
		return
	}
	if _, exists := stats[name]; !exists {
		apiError(c, http.StatusNotFound, "collection not found")
		return
	}
	result, err := a.generateCollectionCover(c.Request.Context(), name, coverOptions{})
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if result.Skipped {
		apiError(c, http.StatusNotFound, result.Reason)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.File(result.Path)
}

// adminCollectionCoverRevert 撤销生效记录，让合集回退到代表海报。
func (a *App) adminCollectionCoverRevert(c *gin.Context) {
	var req struct {
		Collection string `json:"collection"`
		All        bool   `json:"all"`
	}
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil {
		apiError(c, http.StatusBadRequest, "invalid revert request")
		return
	}
	for key := range raw {
		if key != "collection" && key != "all" {
			apiError(c, http.StatusBadRequest, "invalid revert fields")
			return
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		apiError(c, http.StatusBadRequest, "invalid revert request")
		return
	}
	req.Collection = strings.TrimSpace(req.Collection)
	if req.All == (req.Collection != "") {
		apiError(c, http.StatusBadRequest, "provide either collection or all")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	names := []string{req.Collection}
	if req.All {
		covers, err := a.db.AppliedCovers()
		if err != nil {
			apiError(c, http.StatusServiceUnavailable, "applied covers unavailable")
			return
		}
		names = names[:0]
		for name := range covers {
			names = append(names, name)
		}
	}
	reverted := 0
	for _, name := range names {
		cover, ok, err := a.db.AppliedCover(name)
		if err != nil {
			apiError(c, http.StatusServiceUnavailable, "applied covers unavailable")
			return
		}
		if !ok {
			continue
		}
		if err := a.db.ClearAppliedCover(name); err != nil {
			apiError(c, http.StatusServiceUnavailable, "revert failed")
			return
		}
		a.invalidateImageTag(cover.Path)
		reverted++
	}
	a.invalidateAppliedCovers()
	c.JSON(http.StatusOK, gin.H{"reverted": reverted})
}
