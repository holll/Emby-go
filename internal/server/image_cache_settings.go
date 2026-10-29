package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// settingImageCacheMaxBytes 是设置页持久化的图片缓存容量上限（字节）。0 表示不限制。
// 它覆盖配置文件里的 image_cache.max_bytes：配置是部署默认值，设置页是运行期决策。
const settingImageCacheMaxBytes = "image_cache.max_bytes"

// imageCacheMaxBytesLimit 是设置页允许的最大值（64 GiB）。再大就没有意义了：
// 变体是纯派生数据，真需要无上限就直接填 0。
const imageCacheMaxBytesLimit int64 = 64 << 30

// imageCacheTrimWait 是保存设置后等待清理完成的上限：目录很大时不能让
// 一次保存请求无限期挂着，超时后由后台清理继续，响应照常返回当前统计。
const imageCacheTrimWait = 2 * time.Second

type imageCacheSettings struct {
	Dir             string `json:"dir"`
	Enabled         bool   `json:"enabled"`
	MaxBytes        int64  `json:"max_bytes"`
	UsedBytes       int64  `json:"used_bytes"`
	Files           int    `json:"files"`
	MaxAllowedBytes int64  `json:"max_allowed_bytes"`
	ConfigETag      string `json:"config_etag"`
}

// effectiveImageCacheMaxBytes 把配置文件的值归一成设置页语义：负数（不限制）记 0。
func effectiveImageCacheMaxBytes(cfg config.Config) int64 {
	if v := cfg.ImageCache.ApplyDefaults().MaxBytes; v > 0 {
		return v
	}
	return 0
}

// loadImageCacheMaxBytes 读取设置页保存的容量上限；第二个返回值表示是否设置过。
// 坏值按「未设置」处理并报错，让调用方沿用配置文件而不是把服务起不来。
func loadImageCacheMaxBytes(db *store.Store) (int64, bool, error) {
	settings, err := db.Settings()
	if err != nil {
		return 0, false, errors.New("image cache settings unavailable")
	}
	raw, ok := settings[settingImageCacheMaxBytes]
	if !ok {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 || v > imageCacheMaxBytesLimit {
		return 0, false, errors.New("invalid image cache settings")
	}
	return v, true, nil
}

func imageCacheETag(maxBytes int64) string { return fmt.Sprintf("\"ic1-%d\"", maxBytes) }

// snapshot 组装设置页需要的当前状态。调用方需持有 a.imageCacheMu。
func (a *App) imageCacheSnapshot() imageCacheSettings {
	st, err := a.imgDisk.Stats()
	if err != nil {
		// 统计失败不影响读写：把错误降级成 0，页面至少能看到目录和上限。
		slog.Debug("统计缩略图缓存占用失败", "error", err)
	}
	return imageCacheSettings{
		Dir:             a.imgDisk.Dir(),
		Enabled:         a.imgDisk.Enabled(),
		MaxBytes:        a.imageCacheMaxBytes,
		UsedBytes:       st.Bytes,
		Files:           st.Files,
		MaxAllowedBytes: imageCacheMaxBytesLimit,
		ConfigETag:      imageCacheETag(a.imageCacheMaxBytes),
	}
}

func (a *App) adminImageCacheSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	a.imageCacheMu.Lock()
	defer a.imageCacheMu.Unlock()
	c.JSON(http.StatusOK, a.imageCacheSnapshot())
}

func (a *App) adminSaveImageCacheSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !a.ready.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "image cache settings unavailable"})
		return
	}
	var req struct {
		MaxBytes *int64 `json:"max_bytes"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) || req.MaxBytes == nil || *req.MaxBytes < 0 || *req.MaxBytes > imageCacheMaxBytesLimit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image cache settings"})
		return
	}
	if !a.imgDisk.Enabled() {
		c.JSON(http.StatusConflict, gin.H{"code": "IMAGE_CACHE_DISABLED", "error": "image cache is disabled by configuration"})
		return
	}
	tag := c.GetHeader("If-Match")
	if tag == "" {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "image cache settings precondition required"})
		return
	}
	a.imageCacheMu.Lock()
	defer a.imageCacheMu.Unlock()
	if tag != imageCacheETag(a.imageCacheMaxBytes) {
		c.JSON(http.StatusPreconditionFailed, gin.H{"code": "IMAGE_CACHE_SETTINGS_CHANGED", "error": "image cache settings changed; reload before saving"})
		return
	}
	if err := a.db.SetSetting(settingImageCacheMaxBytes, strconv.FormatInt(*req.MaxBytes, 10)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "image cache settings save failed"})
		return
	}
	a.imageCacheMaxBytes = *req.MaxBytes
	a.imgDisk.SetMaxBytes(*req.MaxBytes)
	// 上限调小后立刻清理，但不让保存请求无限期等待——目录很大时后台继续。
	done := make(chan struct{})
	go func() {
		defer close(done)
		removed, freed, err := a.imgDisk.Trim()
		if err != nil {
			slog.Warn("按新上限清理缩略图缓存失败", "error", err)
			return
		}
		if removed > 0 {
			slog.Info("按新上限清理缩略图缓存", "removed", removed, "freed_bytes", freed)
		}
	}()
	select {
	case <-done:
	case <-time.After(imageCacheTrimWait):
	}
	c.JSON(http.StatusOK, a.imageCacheSnapshot())
}

// adminTrimImageCache 立即清理一次缓存目录（过期 + 超限），返回删除统计。
func (a *App) adminTrimImageCache(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !a.imgDisk.Enabled() {
		c.JSON(http.StatusConflict, gin.H{"code": "IMAGE_CACHE_DISABLED", "error": "image cache is disabled by configuration"})
		return
	}
	// 清理可能走几十万个文件，先不持 imageCacheMu，避免设置页读取被拖住。
	removed, freed, err := a.imgDisk.Trim()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "image cache cleanup failed"})
		return
	}
	a.imageCacheMu.Lock()
	snap := a.imageCacheSnapshot()
	a.imageCacheMu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"removed":     removed,
		"freed_bytes": freed,
		"files":       snap.Files,
		"used_bytes":  snap.UsedBytes,
		"config_etag": snap.ConfigETag,
	})
}
