package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

func editNFOPath(m store.Movie) string {
	if p := strings.TrimSpace(m.NFOPath); p != "" {
		return p
	}
	return strings.TrimSuffix(m.SourcePath, filepath.Ext(m.SourcePath)) + ".nfo"
}

func nfoDigest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// finishEditedNFO is called with the NFO write gate held, after a successful
// file replacement, or an explicit digest-checked recovery. Never rewrites NFO.
// This is an R1 manual recovery contract, not a durable repair queue.
func (a *App) finishEditedNFO(c *gin.Context, m store.Movie, path, expectedDigest string) {
	digest, err := nfoDigest(path)
	if expectedDigest != "" && err == nil && digest != expectedDigest {
		c.JSON(http.StatusConflict, gin.H{"code": "NFO_CHANGED", "error": "NFO 已变化，请重新打开条目核对当前文件；未覆盖文件"})
		return
	}
	manual := "pending"
	code := "NFO_READ_FAILED"
	if err == nil {
		manual, code, err = a.indexEditedNFO(m, path, digest)
	}
	a.cache.Clear()
	if err != nil {
		if markErr := a.db.MarkEditFailure(m.ID, code, time.Now()); markErr != nil {
			slog.Warn("记录索引失败待恢复项失败", "movie_id", m.ID, "error", markErr)
		}
		slog.Warn("元数据文件已保存但索引未完成", "movie_id", m.ID, "stage", code, "error", err)
		body := gin.H{"status": "partial_success", "saved": true, "indexed": false, "manual_status": manual,
			"warnings": []gin.H{{"code": code, "retryable": true}}}
		if digest != "" {
			body["recovery"] = gin.H{"nfo_sha256": digest}
		}
		c.JSON(http.StatusOK, body)
		return
	}
	if err := a.db.ClearEditFailure(m.ID); err != nil {
		slog.Warn("清除索引失败待恢复项失败", "movie_id", m.ID, "error", err)
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "saved": true, "indexed": true,
		"manual_status": "saved", "warnings": []gin.H{}})
}

func (a *App) indexEditedNFO(m store.Movie, path, digest string) (string, string, error) {
	if _, err := nfo.Read(path); err != nil {
		return "pending", "NFO_READ_FAILED", err
	}
	// Publish saved manual intent with its version, even if reindexing later fails.
	// Locks live in NFO, so repeated marks must still invalidate cached views.
	if err := a.db.MarkIndexedManual(m.SourcePath, path); err != nil {
		return "pending", "MANUAL_MARK_FAILED", err
	}
	lib, err := a.db.Library(m.LibraryID)
	if err != nil {
		return "saved", "LIBRARY_READ_FAILED", err
	}
	// RescanOne may skip/delete an invalid source, or report Failed without error.
	// Do not run that destructive branch as part of recovery of an already saved file.
	source, err := scanner.ReadSource(m.SourcePath)
	if err != nil {
		return "saved", "SOURCE_UNAVAILABLE", err
	}
	if !scanner.ValidHTTP(source) {
		return "saved", "SOURCE_UNAVAILABLE", fmt.Errorf("source is not playable")
	}
	result, err := scanner.ReindexOne(a.db, lib, m.SourcePath)
	if err != nil {
		return "saved", "INDEX_REFRESH_FAILED", err
	}
	if result.Failed != 0 || result.Pending != 0 || result.Skipped != 0 || !slices.Contains(result.IndexedSources, m.SourcePath) {
		return "saved", "INDEX_REFRESH_INCOMPLETE", fmt.Errorf("target metadata was not fully indexed")
	}
	indexed, err := a.db.Movie(m.ID)
	if err != nil {
		return "saved", "INDEX_REFRESH_INCOMPLETE", err
	}
	if indexed.Status != "manual" || filepath.Clean(indexed.NFOPath) != filepath.Clean(path) {
		return "saved", "INDEX_REFRESH_INCOMPLETE", fmt.Errorf("index does not reference the edited NFO")
	}
	current, err := nfoDigest(path)
	if err != nil {
		return "saved", "NFO_READ_FAILED", err
	}
	if current != digest {
		return "saved", "NFO_CHANGED", fmt.Errorf("NFO changed while indexing")
	}
	return "saved", "", nil
}

// adminRecoverEdit re-reads current NFO, never resends the old edit form.
// The digest is a conflict guard, not a credential or persistent recovery ID.
func (a *App) adminRecoverEdit(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, 404, "not found")
		return
	}
	var req struct {
		Digest string `json:"nfo_sha256"`
	}
	if c.ShouldBindJSON(&req) != nil {
		apiError(c, 400, "invalid json")
		return
	}
	b, err := hex.DecodeString(req.Digest)
	if err != nil || len(b) != sha256.Size {
		apiError(c, 400, "nfo_sha256 required")
		return
	}
	if !a.claimNFO("edit") {
		apiError(c, 409, a.nfoBusyOwner()+"正在进行中，请稍后再试")
		return
	}
	defer a.releaseNFO("edit")
	m, err := a.db.Movie(id)
	if err != nil {
		if store.NotFound(err) {
			apiError(c, 404, "not found")
		} else {
			apiError(c, 503, "索引暂不可用，请稍后重试")
		}
		return
	}
	path := editNFOPath(m)
	current, err := nfoDigest(path)
	if err != nil {
		c.JSON(409, gin.H{"code": "NFO_UNAVAILABLE", "error": "NFO 已不可读，请先人工检查源文件；未覆盖文件"})
		return
	}
	if !strings.EqualFold(current, req.Digest) {
		c.JSON(409, gin.H{"code": "NFO_CHANGED", "error": "NFO 已变化，请重新打开条目核对当前文件；未覆盖文件"})
		return
	}
	a.finishEditedNFO(c, m, path, current)
}

// adminEditRecoveryList 返回待恢复的索引失败条目，并标出超过 7 天的数量。
func (a *App) adminEditRecoveryList(c *gin.Context) {
	failures, err := a.db.EditFailures(time.Now())
	if err != nil {
		apiError(c, 503, "索引恢复列表暂不可用，请稍后重试")
		return
	}
	stale := 0
	for _, f := range failures {
		if f.AgeDays > 7 {
			stale++
		}
	}
	c.JSON(200, gin.H{"items": failures, "total": len(failures), "stale_over_7d": stale})
}
