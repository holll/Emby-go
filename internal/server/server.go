package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/store"
)

type App struct {
	cfg        config.Config
	db         *store.Store
	router     *gin.Engine
	cache      cache.Cache
	adminMu    sync.RWMutex // 保护 adminName：初始化/登录写，其它 handler 并发读
	adminName  string
	serverID   string
	serverName string
	taskMu     sync.Mutex
	tasks      []task
	nextTaskID int64

	// 进程内短缓存：图片 mtime tag 与 NFO 流信息解析结果，
	// 避免列表/详情请求对媒体盘反复 stat / XML 解析（媒体盘可能较慢）。
	tagMu sync.Mutex
	tags  map[string]tagEntry
	nfoMu sync.Mutex
	nfos  map[string]nfoCacheEntry

	// 扫描进度：POST /scan 执行期间由进度回调写入，GET /scan/progress 轮询读取。
	scanMu     sync.RWMutex
	scanStatus scanStatus

	// 未实现端点的探测记录去重：客户端启动期会反复请求同一路径，
	// 只需记下「哪些端点被调用过」，避免每个 404 都写一次库。
	probeMu   sync.Mutex
	probeSeen map[string]struct{}
}

// scanStatus 管理端可轮询的扫描进度快照。
type scanStatus struct {
	Running      bool   `json:"running"`
	LibraryIndex int    `json:"library_index"`
	Libraries    int    `json:"libraries"`
	LibraryName  string `json:"library_name"`
	Total        int    `json:"total"`
	Done         int    `json:"done"`
	Current      string `json:"current"`
	Success      int    `json:"success"`
	Pending      int    `json:"pending"`
	Incompatible int    `json:"incompatible"`
	Failed       int    `json:"failed"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	Error        string `json:"error,omitempty"`
}

// tagEntry / nfoCacheEntry 为上述短缓存的条目（neg 表示负缓存，TTL 更短）。
type tagEntry struct {
	tag string
	ts  time.Time
	neg bool
}
type nfoCacheEntry struct {
	streams []gin.H
	ts      time.Time
	neg     bool
}

// New 强制 Redis 为缓存后端：redis_addr 必填，连接失败拒绝启动。
func New(cfg config.Config) (*App, error) {
	if cfg.RedisAddr == "" {
		return nil, errors.New("缺少 redis_addr：Redis 为必选缓存后端")
	}
	redisCache := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err := redisCache.Ping(); err != nil {
		return nil, fmt.Errorf("redis 连接失败(%s): %w", cfg.RedisAddr, err)
	}
	return newApp(cfg, redisCache)
}

// newApp 供测试注入任意 cache 实现（如内存版），生产路径不走这里。
func newApp(cfg config.Config, cacheStore cache.Cache) (*App, error) {
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, db: db, cache: cacheStore, tags: make(map[string]tagEntry), nfos: make(map[string]nfoCacheEntry), probeSeen: make(map[string]struct{})}
	a.serverName = cfg.ServerName
	if a.serverName == "" {
		a.serverName = "Emby-go"
	}
	a.serverID = cfg.ServerID
	if a.serverID == "" {
		// 未显式配置则在本库生成稳定 UUID，避免重启漂移（客户端以 ServerId 识别实例）。
		a.serverID = a.db.Version("server:id")
		if a.serverID == "" {
			buf := make([]byte, 16)
			if _, err := rand.Read(buf); err == nil {
				a.serverID = hex.EncodeToString(buf)
				_ = a.db.SetKV("server:id", a.serverID)
			} else {
				a.serverID = "emby-go"
			}
		}
		// 回写配置文件，方便纳入版本管理/模板；失败不阻断启动（DB 仍是真源）。
		if err := cfg.PersistServerID(a.serverID); err != nil {
			fmt.Printf("[warn] 无法回写 server_id 到配置文件: %v\n", err)
		}
	}
	if initialized, err := db.HasAdministrator(); err != nil {
		db.Close()
		return nil, err
	} else if initialized {
		if a.adminName, err = db.AdministratorName(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if cfg.Debug {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	middlewares := []gin.HandlerFunc{gin.Logger()}
	if cfg.Debug {
		middlewares = append(middlewares, requestContentLogger())
	}
	middlewares = append(middlewares, gin.Recovery())
	r.Use(middlewares...)
	a.router = r
	a.routes()
	return a, nil
}

func requestContentLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		request, err := httputil.DumpRequest(c.Request, true)
		if err == nil {
			fmt.Printf("[API REQUEST]\n%s\n", string(request))
		}
		c.Next()
	}
}

func (a *App) Close()                { a.db.Close() }
func (a *App) Handler() http.Handler { return a.router }

func (a *App) routes() {
	r := a.router

	// Web 管理前端与内部初始化端点（无 /emby 前缀）
	r.GET("/", a.dashboard)
	r.GET("/admin", a.dashboard)
	r.GET("/web", a.dashboard)
	r.GET("/favicon.ico", a.favicon)
	r.HEAD("/favicon.ico", a.favicon)
	r.GET("/web/login.js", a.webAsset)
	r.GET("/web/app.js", a.webAsset)
	r.GET("/web/style.css", a.webAsset)
	r.GET("/web/vendor/artplayer.min.js", a.webAsset)
	r.GET("/api/auth/status", a.authStatus)
	r.POST("/api/auth/initialize", a.initialize)

	admin := r.Group("/api/admin", a.requireAuth)
	admin.GET("/libraries", a.adminLibraries)
	admin.POST("/libraries", a.adminAddLibrary)
	admin.DELETE("/libraries/:id", a.adminDeleteLibrary)
	admin.POST("/scan", a.adminScan)
	admin.POST("/tasks/scan", a.adminScan)
	admin.GET("/scan/progress", a.adminScanProgress)
	admin.POST("/reindex", a.adminReindex)
	admin.GET("/items", a.adminItems)
	admin.DELETE("/items/:id", a.adminDelete)
	admin.GET("/probe", a.adminProbe)
	admin.DELETE("/probe", a.adminClearProbes)
	admin.GET("/apikeys", a.adminAPIKeys)
	admin.POST("/apikeys", a.adminCreateAPIKey)
	admin.DELETE("/apikeys/:key", a.adminDeleteAPIKey)
	admin.GET("/status", a.adminStatus)
	admin.GET("/settings", a.adminSettings)
	admin.GET("/tasks", a.adminTasks)
	admin.POST("/items/manual", a.adminManual)
	admin.PUT("/items/:id", a.adminEdit)
	admin.POST("/items/:id/reread", a.adminReread)
	admin.POST("/items/:id/images/:kind", a.adminImage)

	// Emby 兼容 API：裸前缀与 /emby 前缀共用同一注册表。
	// 部分客户端以 /emby 为 base（如 https://host/emby），也有直连根路径的。
	a.registerEmby(r.Group(""))
	a.registerEmby(r.Group("/emby"))

	r.NoRoute(a.noRoute)
}

// embyRoute 一条 Emby 兼容端点；auth 为真时挂 requireAuth。
type embyRoute struct {
	method  string
	path    string
	auth    bool
	handler gin.HandlerFunc
}

// embyRoutes 声明全部 Emby 兼容端点。路径按 PascalCase 写，注册时会额外生成
// 全小写变体：真实 Emby 路由大小写不敏感，实测同一端点会被不同客户端以
// /Users/... 与 /users/... 两种写法请求，而 Gin 路由是大小写敏感的。
func (a *App) embyRoutes() []embyRoute {
	return []embyRoute{
		{"POST", "/Users/AuthenticateByName", false, a.authenticate},
		{"GET", "/Users/Public", false, a.publicUsers},
		{"GET", "/Users/Me", true, a.me},
		{"GET", "/Users/:uid/Items/Latest", true, a.latest},
		{"GET", "/Users/:uid/Suggestions", true, a.suggestions},
		{"GET", "/System/Info/Public", false, a.publicInfo},
		{"GET", "/System/Info", true, a.info},
		{"GET", "/System/Configuration", true, a.configuration},
		{"GET", "/System/Endpoint", true, a.endpoint},
		{"GET", "/System/Ping", false, a.ping},
		{"POST", "/System/Ping", false, a.ping},
		{"POST", "/Sessions/Playing/Ping", true, a.noContent},
		{"POST", "/Sessions/Capabilities", true, a.noContent},
		{"POST", "/Sessions/Capabilities/Full", true, a.noContent},
		{"POST", "/Sessions/Logout", true, a.noContent},
		{"GET", "/DisplayPreferences/:pref", true, a.displayPreferences},
		{"POST", "/DisplayPreferences/:pref", true, a.displayPreferences},
		{"GET", "/System/Ext/ServerDomains", true, a.serverDomains},
		{"GET", "/Items/Counts", true, a.counts},
		{"GET", "/Search/Hints", true, a.searchHints},
		{"GET", "/Genres", true, a.genres},
		{"GET", "/Users/:uid/Items/Resume", true, a.resume},
		{"GET", "/Shows/NextUp", true, a.nextUp},
		{"GET", "/Users/:uid/Views", true, a.views},
		{"GET", "/Users/:uid/Items", true, a.items},
		{"GET", "/Items", true, a.rootItems},
		{"GET", "/Users/:uid/Items/:id", true, a.item},
		{"GET", "/Items/:id", true, a.item},
		{"GET", "/Items/:id/Images/:kind", false, a.image},
		{"GET", "/Items/:id/Images/:kind/:index", false, a.image},
		{"GET", "/Items/:id/Images", true, a.imageInfo},
		{"GET", "/Items/:id/Similar", true, a.similar},
		{"GET", "/Items/:id/PlaybackInfo", true, a.playback},
		{"POST", "/Items/:id/PlaybackInfo", true, a.playback},
		{"GET", "/Videos/:id/AdditionalParts", true, a.additionalParts},
		{"GET", "/Episode/:id/IntroSkipperSegments", true, a.emptyList},
		{"GET", "/MediaSegments/:id", true, a.emptyList},
		{"POST", "/Sessions/Playing", true, a.playing},
		{"POST", "/Sessions/Playing/Progress", true, a.playing},
		{"POST", "/Sessions/Playing/Stopped", true, a.playing},
		{"POST", "/Users/:uid/PlayedItems/:id", true, a.played},
		{"DELETE", "/Users/:uid/PlayedItems/:id", true, a.unplayed},
		{"POST", "/Users/:uid/FavoriteItems/:id", true, a.favorite},
		{"DELETE", "/Users/:uid/FavoriteItems/:id", true, a.unfavorite},
		{"POST", "/Users/:uid/FavoriteItems/:id/Delete", true, a.unfavorite},
		{"POST", "/Users/:uid/Items/:id/Rating", true, a.rating},
		{"DELETE", "/Users/:uid/Items/:id/Rating", true, a.unrate},
		{"POST", "/Users/:uid/Items/:id/Rating/Delete", true, a.unrate},
		{"POST", "/Users/:uid/Items/:id/HideFromResume", true, a.hideFromResume},
	}
}

// registerEmby 注册全部 Emby 兼容路由，每条同时挂 PascalCase 与全小写变体。
func (a *App) registerEmby(g *gin.RouterGroup) {
	for _, route := range a.embyRoutes() {
		handlers := make([]gin.HandlerFunc, 0, 2)
		if route.auth {
			handlers = append(handlers, a.requireAuth)
		}
		handlers = append(handlers, route.handler)
		g.Handle(route.method, route.path, handlers...)
		if lower := strings.ToLower(route.path); lower != route.path {
			g.Handle(route.method, lower, handlers...)
		}
	}
	registerStreamRoutes(g, a.stream)
	registerProxyRoutes(g, a.proxyStream)
}

// registerProxyRoutes 网页播放器代理端点（透传 Range），同样注册大小写与可选扩展名。
func registerProxyRoutes(g *gin.RouterGroup, h gin.HandlerFunc) {
	for _, base := range []string{"/Videos/:id/proxy", "/videos/:id/proxy"} {
		g.GET(base, h)
		g.HEAD(base, h)
		g.GET(base+".:ext", h)
		g.HEAD(base+".:ext", h)
	}
}

// registerStreamRoutes 同时注册 /Videos 与 /videos 大小写，吞掉可选扩展名。
func registerStreamRoutes(g *gin.RouterGroup, h gin.HandlerFunc) {
	for _, base := range []string{"/Videos/:id/stream", "/videos/:id/stream"} {
		g.GET(base, h)
		g.HEAD(base, h)
		g.GET(base+".:ext", h)
		g.HEAD(base+".:ext", h)
	}
}

func (a *App) noRoute(c *gin.Context) {
	if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
		key := c.Request.Method + " " + c.Request.URL.Path
		a.probeMu.Lock()
		_, seen := a.probeSeen[key]
		if !seen {
			a.probeSeen[key] = struct{}{}
		}
		a.probeMu.Unlock()
		// 只记首次：客户端探测期同一端点会反复命中，逐次写库会拖慢 404 响应。
		if !seen {
			body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 4096))
			_ = a.db.Probe(c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery, string(body))
		}
	}
	c.JSON(404, gin.H{"error": "not found"})
}
