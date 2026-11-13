package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// libraryCoverRequest 是媒体库封面 run 端点的请求体，字段含义与合集版一致，
// 只是目标换成库 id（库名可以改，id 不会）。
type libraryCoverRequest struct {
	Libraries  []int64 `json:"libraries"`
	All        bool    `json:"all"`
	Apply      bool    `json:"apply"`
	Overwrite  bool    `json:"overwrite"`
	Quality    *int    `json:"quality"`
	Width      *int    `json:"width"`
	Height     *int    `json:"height"`
	Reflection *bool   `json:"reflection"`
}

func decodeLibraryCoverRequest(r io.Reader) (libraryCoverRequest, error) {
	var req libraryCoverRequest
	data, raw, err := artworkObject(r)
	if err != nil {
		return req, err
	}
	for key := range raw {
		switch key {
		case "libraries", "all", "apply", "overwrite", "quality", "width", "height", "reflection":
		default:
			return req, errors.New("invalid library cover fields")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		return req, errors.New("invalid library cover fields")
	}
	if req.All == (len(req.Libraries) > 0) {
		return req, errors.New("provide either libraries or all")
	}
	if len(req.Libraries) > collectionCoverMaxBatch {
		return req, errors.New("too many libraries")
	}
	for _, id := range req.Libraries {
		if id <= 0 {
			return req, errors.New("invalid library id")
		}
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

func (req libraryCoverRequest) options(mode string) coverOptions {
	return coverOptionsFrom(req.Quality, req.Width, req.Height, req.Reflection, req.Overwrite, mode)
}

func (a *App) adminLibraryCoverRun(c *gin.Context) {
	req, err := decodeLibraryCoverRequest(c.Request.Body)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	targets := a.allLibraryTargets()
	if !req.All {
		targets = make([]coverTarget, 0, len(req.Libraries))
		for _, id := range req.Libraries {
			library, err := a.db.Library(id)
			if err != nil {
				apiError(c, http.StatusNotFound, "library not found")
				return
			}
			targets = append(targets, coverTarget{Kind: coverTargetLibrary, Name: library.Name, ID: library.ID})
		}
	}
	mode := "preview"
	if req.Apply {
		mode = "apply"
	}
	accepted, err := a.startCoverTask(targets, req.options(mode), mode)
	if err != nil {
		workerGateAPIError(c, err, http.StatusConflict)
		return
	}
	c.JSON(http.StatusAccepted, accepted)
}

// adminLibraryCoverPreview 当场生成（或复用缓存）一个媒体库的封面并回传字节。
// 它不写库：预览与生效是两件事。
func (a *App) adminLibraryCoverPreview(c *gin.Context) {
	id, err := parseLibraryID(c.Query("library"))
	if err != nil {
		apiError(c, http.StatusBadRequest, "invalid library")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	result, err := a.generateLibraryCover(c.Request.Context(), id, coverOptions{})
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

// adminLibraryCoverRevert 撤销生效记录，让媒体库回退到根目录海报或代表图。
func (a *App) adminLibraryCoverRevert(c *gin.Context) {
	var req struct {
		Library *int64 `json:"library"`
		All     bool   `json:"all"`
	}
	data, raw, err := artworkObject(c.Request.Body)
	if err != nil {
		apiError(c, http.StatusBadRequest, "invalid revert request")
		return
	}
	for key := range raw {
		if key != "library" && key != "all" {
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
	if req.All == (req.Library != nil) || (req.Library != nil && *req.Library <= 0) {
		apiError(c, http.StatusBadRequest, "provide either library or all")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	ids := []int64{}
	if req.All {
		covers, err := a.db.AppliedLibraryCovers()
		if err != nil {
			apiError(c, http.StatusServiceUnavailable, "applied covers unavailable")
			return
		}
		for id := range covers {
			ids = append(ids, id)
		}
	} else {
		ids = append(ids, *req.Library)
	}
	reverted := 0
	for _, id := range ids {
		cover, ok, err := a.db.AppliedLibraryCover(id)
		if err != nil {
			apiError(c, http.StatusServiceUnavailable, "applied covers unavailable")
			return
		}
		if !ok {
			continue
		}
		if err := a.db.ClearAppliedLibraryCover(id); err != nil {
			apiError(c, http.StatusServiceUnavailable, "revert failed")
			return
		}
		a.invalidateImageTag(cover.Path)
		reverted++
	}
	a.invalidateAppliedLibraryCovers()
	c.JSON(http.StatusOK, gin.H{"reverted": reverted})
}

// parseLibraryID 解析 ?library= 的库 id。
func parseLibraryID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("missing library")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid library")
	}
	return id, nil
}
