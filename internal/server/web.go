package server

import (
	"embed"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed web/login.html web/index.html web/login.js web/app.js web/style.css web/favicon.ico
var webFiles embed.FS

// favicon 返回站点图标（与真机同款），浏览器请求 /favicon.ico 时命中。
func (a *App) favicon(c *gin.Context) {
	data, err := webFiles.ReadFile("web/favicon.ico")
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "image/x-icon", data)
}

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
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

func (a *App) webAsset(c *gin.Context) {
	name := "web/" + strings.TrimPrefix(c.Request.URL.Path, "/web/")
	data, err := webFiles.ReadFile(name)
	if err != nil {
		c.Status(http.StatusNotFound)
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

type webRouteRegistrar interface {
	GET(string, ...gin.HandlerFunc) gin.IRoutes
}
