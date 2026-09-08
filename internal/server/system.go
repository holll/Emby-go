package server

import (
	"net/http"
	"runtime"

	"github.com/gin-gonic/gin"
)

// embyVersion 对外通告的 Emby 协议版本。客户端会按此版本做能力判定，
// 版本过低会被部分客户端拒绝连接。本项目对齐 docs/emby_openapi.json 的 4.9.5.0 契约。
const embyVersion = "4.9.5.0"

func (a *App) publicInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LocalAddresses":  []string{},
		"RemoteAddresses": []string{},
		"ServerName":      a.serverName,
		"Version":         embyVersion,
		"ProductName":     "Emby-go",
		"Id":              a.serverID,
	})
}

func (a *App) info(c *gin.Context) {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	osName := map[string]string{"windows": "Windows", "linux": "Linux", "darwin": "macOS"}[runtime.GOOS]
	if osName == "" {
		osName = runtime.GOOS
	}
	local := scheme + "://" + c.Request.Host
	c.JSON(http.StatusOK, gin.H{
		"Id":                         a.serverID,
		"ServerName":                 a.serverName,
		"Version":                    embyVersion,
		"OperatingSystem":            runtime.GOOS,
		"OperatingSystemDisplayName": osName,
		"HasPendingRestart":          false,
		"IsShuttingDown":             false,
		"IsInMaintenanceMode":        false,
		"SupportsHttps":              c.Request.TLS != nil,
		"SupportsAutoRunAtStartup":   false,
		"CanSelfRestart":             false,
		"CanSelfUpdate":              false,
		"SupportsLibraryMonitor":     false,
		"SystemUpdateLevel":          "Release",
		"LocalAddress":               local,
		"LocalAddresses":             []string{local},
		"RemoteAddresses":            []string{},
		"WanAddress":                 local,
		"HttpServerPortNumber":       0,
		"HttpsPortNumber":            0,
	})
}

func (a *App) configuration(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"EnableUPnP":                     false,
		"EnableRemoteAccess":             false,
		"EnableDashboardResponseCaching": true,
	})
}

// ping 供客户端探测服务存活：真实 Emby 返回纯文本 "Emby Server"。
func (a *App) ping(c *gin.Context) { c.String(http.StatusOK, "Emby Server") }

// noContent 用于客户端「只关心 2xx」的探针型端点（Capabilities 上报、播放 Ping 等）。
func (a *App) noContent(c *gin.Context) { c.Status(http.StatusNoContent) }

// endpoint 返回客户端关心的连通性描述（与真实 Emby 形态对齐）。
func (a *App) endpoint(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"IsLocal": false, "IsInNetwork": false})
}

// displayPreferences 最小 DisplayPreferencesDto：
// 客户端在浏览媒体库时会读写展示偏好（排序/视图状态），缺失会导致部分界面回退或报错。
func (a *App) displayPreferences(c *gin.Context) {
	client := c.Query("client")
	if client == "" {
		client = "emby"
	}
	id := c.Param("pref")
	if id == "" {
		id = "usersettings"
	}
	c.JSON(http.StatusOK, gin.H{
		"Id":               id,
		"Client":           client,
		"SortBy":           "SortName",
		"SortOrder":        "Ascending",
		"RememberIndexing": false,
		"CustomPrefs":      gin.H{},
	})
}

func (a *App) serverDomains(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": a.cfg.ServerDomains, "ok": true})
}
