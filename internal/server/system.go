package server

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"

	"emby-go/internal/config"
)

// embyVersion 对外通告的 Emby 协议版本。客户端会按此版本做能力判定，
// 版本过低会被部分客户端拒绝连接。本项目对齐 docs/emby_openapi.json 的 4.9.5.0 契约。
const embyVersion = "4.9.5.0"

// publicInfo 严格按文档的 PublicSystemInfo 七字段输出：
// LocalAddress / LocalAddresses / WanAddress / RemoteAddresses / ServerName / Version / Id。
// 不含 ProductName —— 那是本项目的私有字段，官方模型里没有，客户端按严格模式
// 反序列化时属于未知字段。
func (a *App) publicInfo(c *gin.Context) {
	scheme := requestScheme(c)
	local := scheme + "://" + c.Request.Host
	c.JSON(http.StatusOK, gin.H{
		"LocalAddress":    local,
		"LocalAddresses":  []string{local},
		"WanAddress":      local,
		"RemoteAddresses": []string{},
		"ServerName":      a.serverName,
		"Version":         embyVersion,
		"Id":              a.serverID,
	})
}

// requestScheme 判定客户端实际使用的协议。服务通常跑在 nginx 之后、由 nginx 终止 TLS，
// 此时 c.Request.TLS 恒为 nil，只看 TLS 会把 https 站点通告成 http
// （LocalAddress/WanAddress 错、SupportsHttps=false），而客户端会据此做连接回退。
// 故优先采信 nginx 传来的 X-Forwarded-Proto，没有才回退到 TLS 探测。
func requestScheme(c *gin.Context) string {
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		// 多级代理时可能是 "https, http"，取最外层（第一个）。
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		if proto = strings.TrimSpace(strings.ToLower(proto)); proto == "http" || proto == "https" {
			return proto
		}
	}
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}

// info 按文档的 SystemInfo 模型补齐全部字段。缺字段会让严格反序列化的客户端
// 把必填项判空（例如 PackageName/WebSocketPortNumber 为空会被当成异常版本），
// 因此这里显式列出模型里的每一项，本服务没有对应实现的用零值表达。
func (a *App) info(c *gin.Context) {
	scheme := requestScheme(c)
	osName := map[string]string{"windows": "Windows", "linux": "Linux", "darwin": "macOS"}[runtime.GOOS]
	if osName == "" {
		osName = runtime.GOOS
	}
	local := scheme + "://" + c.Request.Host
	// 数据目录与产物目录都挂在数据库同级目录下，这是本项目唯一稳定的路径锚点
	// （见 openImageDisk / avatar.DefaultDir 的同类约定）。
	dataDir := filepath.Dir(a.cfg.DBPath)
	c.JSON(http.StatusOK, gin.H{
		"Id":                                   a.serverID,
		"ServerName":                           a.serverName,
		"Version":                              embyVersion,
		"OperatingSystem":                      runtime.GOOS,
		"OperatingSystemDisplayName":           osName,
		"PackageName":                          "emby-go",
		"HasPendingRestart":                    false,
		"IsShuttingDown":                       false,
		"IsInMaintenanceMode":                  false,
		"SupportsHttps":                        scheme == "https",
		"SupportsAutoRunAtStartup":             false,
		"SupportsLibraryMonitor":               false,
		"SupportsLocalPortConfiguration":       false,
		"SupportsWakeServer":                   false,
		"HasImageEnhancers":                    false,
		"HasUpdateAvailable":                   false,
		"CanSelfRestart":                       false,
		"CanSelfUpdate":                        false,
		"CanLaunchWebBrowser":                  false,
		"HardwareAccelerationRequiresPremiere": false,
		"SystemUpdateLevel":                    "Release",
		"LocalAddress":                         local,
		"LocalAddresses":                       []string{local},
		"RemoteAddresses":                      []string{},
		"WanAddress":                           local,
		"HttpServerPortNumber":                 a.cfg.Port,
		"HttpsPortNumber":                      0,
		"WebSocketPortNumber":                  a.cfg.Port,
		"CompletedInstallations":               []gin.H{},
		"WakeOnLanInfo":                        []gin.H{},
		"ProgramDataPath":                      dataDir,
		"ItemsByNamePath":                      dataDir,
		"CachePath":                            filepath.Join(dataDir, "image-cache"),
		"LogPath":                              filepath.Join(dataDir, "logs"),
		"InternalMetadataPath":                 filepath.Join(dataDir, "metadata"),
		"TranscodingTempPath":                  filepath.Join(dataDir, "transcodes"),
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
// 状态码取 200 而非 204：文档 SessionsService/PlaystateService 对这组端点全部写
// 「200（无返回内容）」，部分客户端按 200 判断成功，拿到 204 会当作异常分支。
func (a *App) noContent(c *gin.Context) { c.Status(http.StatusOK) }

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

// serverDomainsResponse 固定 ok 在前、data 在后，与真实 Emby 的响应保持一致。
// 用结构体而非 map：Go 的 map 序列化会按字典序排列，无法保证字段顺序。
type serverDomainsResponse struct {
	OK   bool                  `json:"ok"`
	Data []config.ServerDomain `json:"data"`
}

func (a *App) serverDomains(c *gin.Context) {
	domains := a.cfg.ServerDomains
	if domains == nil {
		domains = []config.ServerDomain{}
	}
	c.JSON(http.StatusOK, serverDomainsResponse{OK: true, Data: domains})
}
