package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/logging"
	"emby-go/internal/metatube"
	"emby-go/internal/scheduler"
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

	// 媒体信息探测任务状态。probeCtx 挂在 rootCtx 之下：
	// 任务在 goroutine 里跑，必须用常驻 context——用请求 context 会在 handler 返回时被取消。
	probeTaskMu sync.RWMutex
	probeStatus probeStatus
	probeCtx    context.Context
	probeCancel context.CancelFunc
	rootCtx     context.Context
	rootCancel  context.CancelFunc

	// 未实现端点的探测记录去重：客户端启动期会反复请求同一路径，
	// 只需记下「哪些端点被调用过」，避免每个 404 都写一次库。
	probeMu   sync.Mutex
	probeSeen map[string]struct{}

	// 计划任务调度器：定义存 DB，保存后 Reload 即时生效。
	sched *scheduler.Scheduler

	// nfoGate 是「扫库 / 探测 / 刮削」三者共用的 NFO 写入通道：
	// 三者都会「读全文 → 改局部 → 原子写」同一批 NFO，并发会互相覆盖；
	// 刮削与整库扫描并行时 DeleteMissingSources 还可能误删。nfoOwner 记录当前持有者。
	nfoGate  sync.Mutex
	nfoOwner string

	// 刮削任务状态。scratchCtx 挂在 rootCtx 之下，与探测任务同样的理由：
	// 任务在 goroutine 里跑，不能用请求 context（handler 返回即取消）。
	scrapeTaskMu sync.RWMutex
	scrapeStatus scrapeStatus
	scrapeCtx    context.Context
	scrapeCancel context.CancelFunc
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
	size    int64 // NFO <fileinfo><size>：媒体文件字节数（探测写入）
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
	a.rootCtx, a.rootCancel = context.WithCancel(context.Background())
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
			slog.Warn("无法回写 server_id 到配置文件", "error", err)
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
	middlewares := []gin.HandlerFunc{requestLogger()}
	if cfg.Debug {
		middlewares = append(middlewares, requestContentLogger())
	}
	// 恢复中间件在 panic 时打印堆栈，写入程序日志出口（同时保留控制台），
	// 否则崩溃信息只会留在终端、落不进日志文件。
	middlewares = append(middlewares, gin.RecoveryWithWriter(logging.AppWriter()))
	r.Use(middlewares...)
	a.router = r
	a.routes()

	// 计划任务调度器挂在常驻 rootCtx 之下：Close 取消 ctx 可中止在跑的计划任务。
	a.sched = scheduler.New(a.rootCtx, db, a.runScheduledTask)
	if err := a.sched.Reload(); err != nil {
		slog.Warn("装载计划任务失败", "error", err)
	}
	a.sched.Start()

	// 启动自检：MetaTube 不可达只告警不阻断启动（与 ffprobe 探测的做法一致）——
	// 元数据服务是外部依赖，它挂了不该让整个媒体库服务起不来。
	go a.checkScrapeBackend()
	return a, nil
}

// checkScrapeBackend 启动时探一次 MetaTube 连通性。
func (a *App) checkScrapeBackend() {
	cfg := a.scrapeConfig()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(a.rootCtx, 10*time.Second)
	defer cancel()
	if err := metatube.New(cfg.BaseURL, cfg.Token, cfg.Timeout()).Health(ctx); err != nil {
		slog.Warn("启动自检：MetaTube 不可达（不影响启动，刮削任务会失败）", "url", cfg.BaseURL, "error", err)
		return
	}
	slog.Info("启动自检：MetaTube 可达", "url", cfg.BaseURL)
}

// Close 先停调度与在跑的探测任务再关库，避免后台 goroutine 继续写 NFO / 访问已关闭的 DB。
func (a *App) Close() {
	if a.sched != nil {
		a.sched.Stop()
	}
	a.probeTaskMu.Lock()
	if a.probeCancel != nil {
		a.probeCancel()
	}
	a.probeTaskMu.Unlock()
	a.rootCancel()
	a.db.Close()
}
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
	// 刮削预览用的图片代理刻意不挂鉴权：它要能直接放进 <img src>，而浏览器不会给
	// 图片请求带自定义头（X-Emby-Token），挂鉴权只会让缩略图全部 401。
	// 上游 MetaTube 的图片端点本身也是公开的（Jellyfin 插件同样直接引用），
	// 这里只做「限定 kind + 限定目标服务 + 限时限量」的转发，不放大暴露面。
	r.GET("/api/admin/scrape/image", a.adminScrapeImage)

	admin := r.Group("/api/admin", a.requireAuth)
	admin.GET("/libraries", a.adminLibraries)
	admin.POST("/libraries", a.adminAddLibrary)
	admin.DELETE("/libraries/:id", a.adminDeleteLibrary)
	admin.POST("/scan", a.adminScan)
	admin.POST("/tasks/scan", a.adminScan)
	admin.GET("/scan/progress", a.adminScanProgress)
	// 媒体信息探测（与扫库相互独立）。路径用 probe/media 以区别于已有的
	// /probe——后者记录客户端调用过的未实现端点，语义完全不同。
	admin.POST("/probe/media", a.adminProbeMedia)
	admin.GET("/probe/media/progress", a.adminProbeMediaProgress)
	admin.POST("/probe/media/cancel", a.adminProbeMediaCancel)
	admin.POST("/reindex", a.adminReindex)
	admin.GET("/items", a.adminItems)
	admin.GET("/items/:id/detail", a.adminItemDetail)
	admin.DELETE("/items/:id", a.adminDelete)
	admin.GET("/probe", a.adminProbe)
	admin.DELETE("/probe", a.adminClearProbes)
	admin.GET("/apikeys", a.adminAPIKeys)
	admin.POST("/apikeys", a.adminCreateAPIKey)
	admin.DELETE("/apikeys/:key", a.adminDeleteAPIKey)
	admin.GET("/status", a.adminStatus)
	admin.GET("/settings", a.adminSettings)
	admin.GET("/tasks", a.adminTasks)
	// 刮削（R4）：配置 + 批量/头像任务 + 单条预览确认 + 图片代理。
	admin.GET("/scrape/settings", a.adminScrapeSettings)
	admin.PUT("/scrape/settings", a.adminSaveScrapeSettings)
	admin.POST("/scrape/test", a.adminScrapeTest)
	admin.GET("/scrape/candidates", a.adminScrapeCandidates)
	admin.POST("/scrape/run", a.adminScrapeRun)
	admin.POST("/scrape/avatars", a.adminScrapeAvatars)
	admin.GET("/scrape/progress", a.adminScrapeProgress)
	admin.POST("/scrape/cancel", a.adminScrapeCancel)
	// 计划任务（cron）：保存/启停后调度器即时重建，无需重启。
	admin.GET("/scheduled", a.adminScheduledTasks)
	admin.POST("/scheduled", a.adminCreateScheduledTask)
	admin.POST("/scheduled/validate", a.adminValidateCron)
	admin.PUT("/scheduled/:id", a.adminUpdateScheduledTask)
	admin.DELETE("/scheduled/:id", a.adminDeleteScheduledTask)
	admin.POST("/scheduled/:id/toggle", a.adminToggleScheduledTask)
	admin.POST("/scheduled/:id/run", a.adminRunScheduledTask)
	admin.POST("/items/manual", a.adminManual)
	admin.PUT("/items/:id", a.adminEdit)
	admin.POST("/items/:id/reread", a.adminReread)
	admin.POST("/items/:id/probe", a.adminProbeMediaItem)
	// 单条手动刮削：预览 → 看 diff → 确认写入（无状态，取消零副作用）。
	admin.GET("/items/:id/scrape/preview", a.adminScrapePreview)
	admin.POST("/items/:id/scrape/inspect", a.adminScrapeInspect)
	admin.POST("/items/:id/scrape", a.adminScrapeConfirm)
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
		{"GET", "/Users/:uid", true, a.userByID},
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
		// 带用户前缀的同一端点：参考客户端（iPlay / openemby_tv / tsukimi）用的是上面那种，
		// 但真实 Emby 两种都提供，部分客户端（如 Yamby）走这种，补上不增加维护成本。
		{"GET", "/Users/:uid/Items/:id/PlaybackInfo", true, a.playback},
		{"POST", "/Users/:uid/Items/:id/PlaybackInfo", true, a.playback},
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
