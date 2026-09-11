package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/scraper"
)

// 刮削配置的设置键（需求 A6 #43）。
// 按需求 B5：**config.yaml 不设 scrape 段**，DB 只存用户改过的值，
// 缺省一律走 scraper.DefaultConfig()——这样以后新增配置项，老库自动获得默认值，无需迁移。
const (
	settingMetatubeURL    = "scrape.metatube_url"
	settingMetatubeToken  = "scrape.metatube_token"
	settingTimeout        = "scrape.timeout_seconds"
	settingConcurrency    = "scrape.concurrency"
	settingDownloadImages = "scrape.download_images"
	settingImageQuality   = "scrape.image_quality"
	settingOverwrite      = "scrape.overwrite"

	settingTranslateTitle   = "scrape.translate.title"
	settingTranslateSummary = "scrape.translate.summary"
	settingTranslateLang    = "scrape.translate.target_lang"
	settingTranslateAPIURL  = "scrape.translate.api_url"
	settingTranslateAPIKey  = "scrape.translate.api_key"
	settingTranslateTimeout = "scrape.translate.timeout_seconds"
)

func settingString(values map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(values[key]); value != "" {
		return value
	}
	return fallback
}

func settingInt(values map[string]string, key string, fallback int) int {
	if value := strings.TrimSpace(values[key]); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

// settingBool 只认明确写过的 true/false；键不存在或写坏了都用默认值。
func settingBool(values map[string]string, key string, fallback bool) bool {
	value := strings.TrimSpace(values[key])
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// scrapeConfig 组装刮削配置：代码默认值 + DB 里用户改过的项。
func (a *App) scrapeConfig() scraper.Config {
	cfg := scraper.DefaultConfig()
	values, err := a.db.Settings()
	if err != nil {
		values = map[string]string{}
	}
	cfg.BaseURL = settingString(values, settingMetatubeURL, cfg.BaseURL)
	cfg.Token = settingString(values, settingMetatubeToken, cfg.Token)
	cfg.TimeoutSeconds = settingInt(values, settingTimeout, cfg.TimeoutSeconds)
	cfg.Concurrency = settingInt(values, settingConcurrency, cfg.Concurrency)
	cfg.DownloadImages = settingBool(values, settingDownloadImages, cfg.DownloadImages)
	cfg.ImageQuality = settingInt(values, settingImageQuality, cfg.ImageQuality)
	cfg.Overwrite = settingBool(values, settingOverwrite, cfg.Overwrite)
	// 头像目录统一走 a.avatarsDir()：详情接口与头像任务必须落在同一个目录，
	// 否则会出现「头像写进去了但详情页看不到」。
	cfg.AvatarsDir = a.avatarsDir()

	cfg.TranslateTitle = settingBool(values, settingTranslateTitle, cfg.TranslateTitle)
	cfg.TranslatePlot = settingBool(values, settingTranslateSummary, cfg.TranslatePlot)
	cfg.Translate.TargetLang = settingString(values, settingTranslateLang, cfg.Translate.TargetLang)
	cfg.Translate.APIURL = settingString(values, settingTranslateAPIURL, cfg.Translate.APIURL)
	cfg.Translate.APIKey = settingString(values, settingTranslateAPIKey, cfg.Translate.APIKey)
	cfg.Translate.Timeout = secondsToDuration(settingInt(values, settingTranslateTimeout, int(cfg.Translate.Timeout.Seconds())))
	return cfg
}

// maskSecret 密钥只写不回显：给出前 4 位 + 固定掩码，够人工核对配的是哪一把，又不足以复原。
func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + "****"
}

func (a *App) adminScrapeSettings(c *gin.Context) {
	cfg := a.scrapeConfig()
	c.JSON(http.StatusOK, gin.H{
		"metatube_url":    cfg.BaseURL,
		"metatube_token":  maskSecret(cfg.Token),
		"timeout_seconds": cfg.TimeoutSeconds,
		"concurrency":     cfg.Concurrency,
		"download_images": cfg.DownloadImages,
		"image_quality":   cfg.ImageQuality,
		"avatars_dir":     cfg.AvatarsDir,
		"overwrite":       cfg.Overwrite,
		"configured":      cfg.Configured(),
		"defaults":        scraper.DefaultConfig(),
		"translate": gin.H{
			"title":           cfg.TranslateTitle,
			"summary":         cfg.TranslatePlot,
			"target_lang":     cfg.Translate.TargetLang,
			"api_url":         cfg.Translate.APIURL,
			"api_key":         maskSecret(cfg.Translate.APIKey),
			"timeout_seconds": int(cfg.Translate.Timeout.Seconds()),
			"configured":      cfg.Translate.Enabled(),
		},
	})
}

// scrapeSettingsRequest 保存配置的入参。密钥字段留空 = 不修改（需求 A6 #42）。
type scrapeSettingsRequest struct {
	MetatubeURL    *string `json:"metatube_url"`
	MetatubeToken  *string `json:"metatube_token"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
	Concurrency    *int    `json:"concurrency"`
	DownloadImages *bool   `json:"download_images"`
	ImageQuality   *int    `json:"image_quality"`
	AvatarsDir     *string `json:"avatars_dir"`
	Overwrite      *bool   `json:"overwrite"`
	Translate      *struct {
		Title          *bool   `json:"title"`
		Summary        *bool   `json:"summary"`
		TargetLang     *string `json:"target_lang"`
		APIURL         *string `json:"api_url"`
		APIKey         *string `json:"api_key"`
		TimeoutSeconds *int    `json:"timeout_seconds"`
	} `json:"translate"`
}

func (a *App) adminSaveScrapeSettings(c *gin.Context) {
	var req scrapeSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	writes := map[string]string{}
	putString := func(key string, value *string) {
		if value == nil {
			return
		}
		writes[key] = strings.TrimSpace(*value)
	}
	putInt := func(key string, value *int, min, max int) error {
		if value == nil {
			return nil
		}
		if *value < min || *value > max {
			return errOutOfRange(key, min, max)
		}
		writes[key] = strconv.Itoa(*value)
		return nil
	}
	putBool := func(key string, value *bool) {
		if value == nil {
			return
		}
		writes[key] = strconv.FormatBool(*value)
	}

	putString(settingMetatubeURL, req.MetatubeURL)
	// 密钥留空或仍是掩码值时不写：避免把界面上的 **** 原样存成新密钥。
	if req.MetatubeToken != nil && !isMasked(*req.MetatubeToken) {
		writes[settingMetatubeToken] = strings.TrimSpace(*req.MetatubeToken)
	}
	putString(settingAvatarsDir, req.AvatarsDir)
	if err := putInt(settingTimeout, req.TimeoutSeconds, 1, 600); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := putInt(settingConcurrency, req.Concurrency, 1, 8); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := putInt(settingImageQuality, req.ImageQuality, 1, 100); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	putBool(settingDownloadImages, req.DownloadImages)
	putBool(settingOverwrite, req.Overwrite)

	if tr := req.Translate; tr != nil {
		putBool(settingTranslateTitle, tr.Title)
		putBool(settingTranslateSummary, tr.Summary)
		putString(settingTranslateLang, tr.TargetLang)
		putString(settingTranslateAPIURL, tr.APIURL)
		if tr.APIKey != nil && !isMasked(*tr.APIKey) {
			writes[settingTranslateAPIKey] = strings.TrimSpace(*tr.APIKey)
		}
		if err := putInt(settingTranslateTimeout, tr.TimeoutSeconds, 1, 300); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	for key, value := range writes {
		if err := a.db.SetSetting(key, value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	a.adminScrapeSettings(c)
}

// isMasked 判断提交上来的密钥是否为界面回显的掩码（掩码值不写库）。
func isMasked(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.Contains(value, "****")
}

func errOutOfRange(key string, min, max int) error {
	return &rangeError{key: key, min: min, max: max}
}

type rangeError struct {
	key      string
	min, max int
}

func (e *rangeError) Error() string {
	return e.key + " 必须在 " + strconv.Itoa(e.min) + "~" + strconv.Itoa(e.max) + " 之间"
}

// secondsToDuration 把秒数换算成 time.Duration（配置项统一以秒存储）。
func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
