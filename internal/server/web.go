package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed web/login.html web/index.html web/login.js web/app.js web/style.css web/favicon.ico web/vendor/artplayer.min.js
var webFiles embed.FS

// assetETags 嵌入静态资源的内容 ETag。文件在构建期固化，启动算一次即可。
// 前端文件名不带 hash/版本号，所以不能上 immutable 长缓存（升级后浏览器
// 连校验都不发，页面永远停在旧版）；改用 ETag + no-cache：每次都校验，
// 命中即 304，代价接近零。
var assetETags = buildAssetETags()

func buildAssetETags() map[string]string {
	names := []string{
		"web/login.js", "web/app.js", "web/style.css",
		"web/vendor/artplayer.min.js", "web/favicon.ico",
	}
	tags := make(map[string]string, len(names))
	for _, name := range names {
		data, err := webFiles.ReadFile(name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		tags[name] = hex.EncodeToString(sum[:16])
	}
	return tags
}

// etagHit 写入 ETag 与缓存策略，返回 true 表示命中 If-None-Match（调用方应答 304）。
func etagHit(c *gin.Context, name, cacheControl string) bool {
	tag, ok := assetETags[name]
	if !ok {
		return false
	}
	c.Header("ETag", `"`+tag+`"`)
	c.Header("Cache-Control", cacheControl)
	return ifNoneMatchHit(c.GetHeader("If-None-Match"), tag)
}

// favicon 返回站点图标（与真机同款），浏览器请求 /favicon.ico 时命中。
// 图标极少变动，给一天缓存 + ETag，过期后由 304 续。
func (a *App) favicon(c *gin.Context) {
	data, err := webFiles.ReadFile("web/favicon.ico")
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if etagHit(c, "web/favicon.ico", "public, max-age=86400,must-revalidate") {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "image/x-icon", data)
}

// dashboard 输出登录页/管理页外壳 HTML。外壳内嵌脚本地址，改版后必须立即生效，
// 故禁止缓存。
func (a *App) dashboard(c *gin.Context) {
	name := "web/login.html"
	if c.Request.URL.Path == "/admin" {
		name = "web/index.html"
	}
	data, err := webFiles.ReadFile(name)
	if err != nil {
		c.String(http.StatusInternalServerError, "web unavailable")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

func (a *App) webAsset(c *gin.Context) {
	name := "web/" + strings.TrimPrefix(c.Request.URL.Path, "/web/")
	data, err := webFiles.ReadFile(name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	// no-cache 不是「不缓存」，而是「每次都要校验」：命中 ETag 直接 304，
	// 既保证升级即时生效，又不重复传输正文。
	if etagHit(c, name, "no-cache") {
		c.Status(http.StatusNotModified)
		return
	}
	contentType := "text/plain; charset=utf-8"
	if len(name) >= 3 && name[len(name)-3:] == ".js" {
		contentType = "application/javascript; charset=utf-8"
	}
	if len(name) >= 4 && name[len(name)-4:] == ".css" {
		contentType = "text/css; charset=utf-8"
	}
	c.Data(http.StatusOK, contentType, data)
}
