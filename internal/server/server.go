package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/logging"
	"emby-go/internal/metatube"
	"emby-go/internal/scheduler"
	"emby-go/internal/store"
	"emby-go/internal/webhook"
)

// defaultMemoryCacheEntries 是内存兜底缓存容量（条目数）。
const defaultMemoryCacheEntries = 4096

type App struct {
	cfg               config.Config
	db                *store.Store
	router            *gin.Engine
	cache             cache.Cache
	webhook           *webhook.Sender
	webhookMu         sync.RWMutex // immutable sender/config snapshots; never held during HTTP delivery
	webhookUpdateMu   sync.Mutex   // serialize persistent saves with shutdown
	webhookConfig     config.WebhookConfig
	webhookClosed     bool
	logRetentionMu    sync.Mutex
	logRetentionDays  int
	applyLogRetention func(int) error
	cacheBackend      string
	cacheOnline       bool
	// ffprobePath/ffprobeError 是启动自检结果，供设置页展示探测能力是否就绪。
	ffprobePath  string
	ffprobeError string
	adminMu      sync.RWMutex // 保护 adminName：初始化/登录写，其它 handler 并发读
	adminName    string
	serverID     string
	serverName   string
	// sessionTTL 是登录会话有效期（0=不过期），来自配置 session_ttl_days。
	sessionTTL time.Duration
	// initAllow 限制首次初始化来源；为空表示不限制。
	initAllow  []netip.Prefix
	taskMu     sync.Mutex
	tasks      []task
	nextTaskID int64

	// 登录/初始化失败限流：进程内固定窗口，按 TCP 对端 IP 与账号计数。
	authLimiter *authRateLimiter

	// 进程内短缓存：图片 mtime tag 与 NFO 流信息解析结果，
	// 避免列表/详情请求对媒体盘反复 stat / XML 解析（媒体盘可能较慢）。
	tagMu sync.Mutex
	tags  map[string]tagEntry
	nfoMu sync.Mutex
	nfos  map[string]nfoCacheEntry

	// 图片链路的进程内缓存：imgMeta 存「影片 id → 图片路径」「演员名 → 头像路径」
	// 这类每次请求都要的元信息（原本每张图一次 DB 查询，单连接下会串行排队）；
	// imgThumb 存按请求尺寸生成的缩略图字节。都只在本进程内有意义，
	// 走 Redis 每张图一次 RTT 不划算，故用本地 LRU。
	imgMeta  *cache.Memory
	imgThumb *cache.Memory
	// thumbSem 限制同时解码 + 缩放 + 编码的数量：纯 CPU 活，一次海报墙
	// 几百张不加限制会把 CPU 打满、拖慢其它请求。
	thumbSem chan struct{}
	// scrapeSem 限制同时向上游拉取的缩略图数量：刮削预览页一次十几张，
	// 不加限制就是十几路并发上游请求，慢上游会把 goroutine 堆起来。
	scrapeSem chan struct{}

	// 扫描进度：POST /scan 执行期间由进度回调写入，GET /scan/progress 轮询读取。
	scanMu     sync.RWMutex
	scanStatus scanStatus

	// D10：异步扫描任务表（进程内，容量 200，重启不恢复）。
	scanTaskMu    sync.Mutex
	scanTasks     map[int64]*asyncScanTask
	scanTaskOrder []int64
	scanTaskIdem  map[string]scanIdemEntry

	// 媒体信息探测任务状态。probeCtx 挂在 rootCtx 之下：
	// 任务在 goroutine 里跑，必须用常驻 context——用请求 context 会在 handler 返回时被取消。
	probeTaskMu sync.RWMutex
	probeStatus probeStatus
	probeCtx    context.Context
	probeCancel context.CancelFunc
	rootCtx     context.Context
	rootCancel  context.CancelFunc
	startupWG   sync.WaitGroup // join startup DB readers before closing the store

	// 未实现端点的探测记录去重：客户端启动期会反复请求同一路径，
	// 只需记下「哪些端点被调用过」，避免每个 404 都写一次库。
	probeMu        sync.Mutex
	probeSeen      map[string]probeSeenEntry
	probeEpoch     uint64
	probeNow       func() time.Time // 测试可注入时钟；生产为 time.Now
	probeTokens    float64          // 诊断写库令牌（burst 上限见 probeWriteBurst）
	probeRefillAt  time.Time
	probePersistMu sync.Mutex // serialize diagnostic commits and successful clears

	startAt      time.Time
	ready        atomic.Bool    // readiness：收到退出信号后立即为 false
	shutdownOnce sync.Once      // BeginShutdown 幂等
	closeOnce    sync.Once      // Close 幂等
	workerWG     sync.WaitGroup // 探测/刮削/立即执行计划等后台 worker，退出时有界 join
	// 周期性统计信息维护（ANALYZE）。analyzeStats 可替换以便测试观察调用。
	maintenanceOnce sync.Once
	analyzeStats    func() error
	schedMu         sync.Mutex
	schedDone       context.Context // scheduler.Stop 返回的完成信号（BeginShutdown 写入）

	// 计划任务调度器：定义存 DB，保存后 Reload 即时生效。
	sched *scheduler.Scheduler

	// nfoGate 是「扫库 / 探测 / 刮削」三者共用的 NFO 写入通道：
	// 三者都会「读全文 → 改局部 → 原子写」同一批 NFO，并发会互相覆盖；
	// 刮削与整库扫描并行时 DeleteMissingSources 还可能误删。nfoOwner 记录当前持有者。
	//
	// nfoRefs 是同一持有者内的重入计数：批量探测与「单条探测」都用 "probe" 身份，
	// 没有计数时先结束的那个会把 owner 清空，另一个的保护就没了。
	nfoGate  sync.Mutex
	nfoOwner string
	nfoRefs  int

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
	Skipped      int    `json:"skipped"`
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
	probed  bool  // NFO 里有可用的 <streamdetails>（详情页「是否已探测」）
	ts      time.Time
	neg     bool
}

// New 按配置选择缓存后端：配置 redis_addr 且可用时用 Redis，
// 未配置或连接失败时回退进程内缓存（单实例语义，多实例不共享、重启失效）。
func New(cfg config.Config) (*App, error) {
	if cfg.RedisAddr == "" {
		slog.Warn("未配置 redis_addr：使用进程内缓存（多实例不共享、重启失效）")
	}
	app, err := newApp(cfg, nil)
	if err != nil {
		return nil, err
	}
	// D04：未设置 setup_code 时提示部署边界，避免把初始化入口直接暴露到公网。
	if cfg.SetupCode == "" {
		slog.Warn("未设置 setup_code：首次初始化前请只监听回环/内网，或通过反向代理限制来源")
	}
	app.startMaintenance()
	app.selfCheckFFProbe()
	return app, nil
}

// newApp 供测试注入任意 cache 实现（如内存版），生产路径不走这里。
func newApp(cfg config.Config, cacheStore cache.Cache) (*App, error) {
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	retentionDays, err := loadLogRetention(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	effectiveWebhook, err := loadWebhookConfig(db, cfg.Webhook)
	if err != nil {
		db.Close()
		return nil, err
	}
	notifier, err := webhook.New(effectiveWebhook)
	if err != nil {
		db.Close()
		return nil, err
	}
	notifierOwned := false
	defer func() {
		if !notifierOwned {
			notifier.Close()
		}
	}()
	cacheBackend, cacheOnline := "memory", false
	if cacheStore == nil {
		if cfg.RedisAddr == "" {
			cacheStore = cache.NewMemory(defaultMemoryCacheEntries)
		} else {
			namespace, err := db.CacheNamespace()
			if err != nil {
				db.Close()
				return nil, err
			}
			redisCache := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, namespace)
			if err := redisCache.Ping(); err != nil {
				slog.Warn("Redis 不可用，回退进程内缓存", "addr", cfg.RedisAddr, "error", err)
				cacheStore = cache.NewMemory(defaultMemoryCacheEntries)
			} else {
				cacheStore = redisCache
				cacheBackend, cacheOnline = "redis", true
			}
		}
	}
	a := &App{
		cfg: cfg, db: db, cache: cacheStore, webhook: notifier, webhookConfig: effectiveWebhook, cacheBackend: cacheBackend, cacheOnline: cacheOnline,
		logRetentionDays: retentionDays, applyLogRetention: logging.SetRetentionDays,
		tags: make(map[string]tagEntry), nfos: make(map[string]nfoCacheEntry),
		probeSeen: make(map[string]probeSeenEntry), probeNow: time.Now,
		startAt:     time.Now(),
		probeTokens: probeWriteBurst, probeRefillAt: time.Now(),
		imgMeta: cache.NewMemory(imageMetaCacheSize), imgThumb: cache.NewMemory(imageThumbCacheSize),
		thumbSem: make(chan struct{}, maxThumbConcurrency), scrapeSem: make(chan struct{}, maxScrapeImageConcurrency),
		authLimiter:  newAuthRateLimiter(),
		scanTasks:    make(map[int64]*asyncScanTask),
		scanTaskIdem: make(map[string]scanIdemEntry),
	}
	a.ready.Store(true)
	a.rootCtx, a.rootCancel = context.WithCancel(context.Background())
	a.analyzeStats = db.Analyze
	a.serverName = cfg.ServerName
	if a.serverName == "" {
		a.serverName = "Emby-go"
	}
	a.sessionTTL = cfg.SessionTTL()
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
	// Framework debug redirect output can contain raw URLs outside middleware.
	// App debug is selected per instance below, not via Gin's global debug mode.
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// 不信任任何代理头：限流与日志按 TCP 对端 IP 计数，X-Forwarded-For 可被伪造。
	// 将来若部署在反向代理后，必须先给出可信代理网段并单独评审。
	for _, cidr := range cfg.InitializeAllowFrom {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize_allow_from 配置无效(%q): %w", cidr, err)
		}
		a.initAllow = append(a.initAllow, prefix.Masked())
	}
	if len(cfg.TrustedProxies) > 0 {
		if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
			db.Close()
			return nil, fmt.Errorf("trusted_proxies 配置无效: %w", err)
		}
	} else {
		_ = r.SetTrustedProxies(nil)
	}
	if cfg.Debug {
		slog.Warn("debug 原始请求日志已开启：密码、token、Cookie、请求体将写入 request 日志，请仅临时排障使用")
	}
	middlewares := append([]gin.HandlerFunc{securityHeaders()}, requestDiagnostics(cfg.Debug, logging.RequestWriter(), logging.AppWriter())...)
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
	a.startupWG.Add(1)
	go func() {
		defer a.startupWG.Done()
		a.checkScrapeBackend()
	}()
	if err := a.applyLogRetention(retentionDays); err != nil {
		slog.Warn("历史日志清理未完成，将在下个日志日重试")
	}
	notifierOwned = true
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
		slog.Warn("启动自检：MetaTube 不可达（不影响启动，刮削任务会失败）", backendHealthLogFields(err)...)
		return
	}
	slog.Info("启动自检：MetaTube 可达")
}

// BeginShutdown 让 readiness 立即失败、停止新调度、取消可取消的后台任务。
// 幂等，只做一次；真正等待使用 WaitBackground，超时由调用方决定强制退出。
func (a *App) BeginShutdown() {
	a.shutdownOnce.Do(func() {
		a.ready.Store(false)
		if a.sched != nil {
			done := a.sched.Stop()
			a.schedMu.Lock()
			a.schedDone = done
			a.schedMu.Unlock()
		}
		a.probeTaskMu.Lock()
		if a.probeCancel != nil {
			a.probeCancel()
		}
		a.probeTaskMu.Unlock()
		a.scrapeTaskMu.Lock()
		if a.scrapeCancel != nil {
			a.scrapeCancel()
		}
		a.scrapeTaskMu.Unlock()
		a.rootCancel()
	})
}

// WaitBackground 在 ctx 预算内等待启动自检、探测/刮削/立即执行 worker 与
// 调度器在跑任务结束。必须先在调用方触发 BeginShutdown，避免等待后又登记新工作。
func (a *App) WaitBackground(ctx context.Context) error {
	a.schedMu.Lock()
	schedDone := a.schedDone
	a.schedMu.Unlock()
	done := make(chan struct{})
	go func() {
		a.workerWG.Wait()
		a.startupWG.Wait()
		if schedDone != nil {
			<-schedDone.Done()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 取消后台任务并关库；幂等，供 defer 与显式退出路径重复调用。
func (a *App) Close() {
	a.closeOnce.Do(func() {
		a.BeginShutdown()
		a.startupWG.Wait()
		a.closeWebhook()
		a.db.Close()
	})
}

// health 是公开的 readiness 探针：只暴露进程是否仍在接纳，不泄露版本/依赖细节。
func (a *App) health(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !a.ready.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "shutting_down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
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
	r.GET("/web/webhook-settings.js", a.webAsset)
	r.GET("/web/log-settings.js", a.webAsset)
	r.GET("/web/style.css", a.webAsset)
	r.GET("/web/vendor/artplayer.min.js", a.webAsset)
	r.GET("/api/health", a.health)
	r.GET("/healthz", a.health)
	r.GET("/api/version", noStore, a.buildInfo)
	r.GET("/api/auth/status", noStore, a.authStatus)
	r.POST("/api/auth/initialize", noStore, a.initialize)
	auth := r.Group("/api/auth", noStore, a.requireAuth)
	auth.POST("/password", a.changePassword)
	// 刮削预览用的图片代理刻意不挂鉴权：它要能直接放进 <img src>，而浏览器不会给
	// 图片请求带自定义头（X-Emby-Token），挂鉴权只会让缩略图全部 401。
	// 上游 MetaTube 的图片端点本身也是公开的（Jellyfin 插件同样直接引用），
	// 这里只做「限定 kind + 限定目标服务 + 限时限量」的转发，不放大暴露面。
	r.GET("/api/admin/scrape/image", a.adminScrapeImage)

	// noStore 必须先于 requireAuth：鉴权失败会 Abort，后面的中间件不再执行。
	admin := r.Group("/api/admin", noStore, a.requireAuth, a.requireAdmin, a.auditAdminAction)
	admin.GET("/libraries", a.adminLibraries)
	admin.POST("/libraries", a.adminAddLibrary)
	admin.DELETE("/libraries/:id", a.adminDeleteLibrary)
	admin.POST("/libraries/:id/move", a.adminMoveLibrary)
	admin.POST("/libraries/:id/cover", a.adminLibraryCover)
	admin.DELETE("/libraries/:id/cover", a.adminLibraryCoverDelete)
	admin.POST("/scan", a.adminScan)
	// D10：/tasks/scan 走异步任务表；旧同步 /scan 保留到验收集通过。
	admin.POST("/tasks/scan", a.adminSubmitScan)
	admin.GET("/tasks/:id", a.adminScanTaskStatus)
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
	admin.GET("/users", a.adminListUsers)
	admin.POST("/users", a.adminCreateUser)
	admin.PUT("/users/:id", a.adminUpdateUser)
	admin.DELETE("/users/:id", a.adminDeleteUser)
	admin.PUT("/users/:id/libraries", a.adminSetUserLibraries)
	admin.GET("/login-events", a.adminLoginEvents)
	admin.GET("/playback-events", a.adminPlaybackEvents)
	admin.GET("/apikeys", a.adminAPIKeys)
	admin.POST("/apikeys", a.adminCreateAPIKey)
	admin.DELETE("/apikeys/:key", a.adminDeleteAPIKey)
	// D01：撤销全部登录会话（API 密钥不受影响）。
	admin.DELETE("/access-tokens", a.adminRevokeAllSessions)
	admin.GET("/audit-events", a.adminAuditEvents)
	admin.GET("/status", a.adminStatus)
	admin.GET("/metrics", a.adminMetrics)
	admin.GET("/logs", a.adminLogs)
	admin.GET("/logs/settings", a.adminLogSettings)
	admin.PUT("/logs/settings", a.adminSaveLogSettings)
	admin.GET("/settings", a.adminSettings)
	admin.GET("/webhook/settings", a.adminWebhookSettings)
	admin.PUT("/webhook/settings", a.adminSaveWebhookSettings)
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
	admin.PATCH("/scheduled/:id", a.adminConditionalScheduledTask)
	admin.DELETE("/scheduled/:id", a.adminDeleteScheduledTask)
	admin.POST("/scheduled/:id/toggle", a.adminToggleScheduledTask)
	admin.POST("/scheduled/:id/run", a.adminRunScheduledTask)
	admin.PUT("/items/:id", a.adminEdit)
	admin.PATCH("/items/:id", a.adminPatch)
	admin.POST("/items/:id/recover-edit", a.adminRecoverEdit)
	admin.GET("/recovery", a.adminEditRecoveryList)
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
		{"POST", "/Sessions/Logout", true, a.logout},
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
		{"GET", "/Items/:id/Collections", true, a.itemCollections},
		{"GET", "/Users/:uid/Items/:id/Collections", true, a.itemCollections},
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

// noStore 给动态响应加禁止缓存头。Emby 客户端会缓存「无 Cache-Control」的 JSON，
// PlaybackInfo / Items 这类响应一旦被缓存，媒体源与播放进度就不再刷新；
// 视频流的 302 与代理字节流同理，必须禁止客户端与中间层缓存。
func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

// registerEmby 注册全部 Emby 兼容路由，每条同时挂 PascalCase 与全小写变体。
func (a *App) registerEmby(g *gin.RouterGroup) {
	for _, route := range a.embyRoutes() {
		handlers := make([]gin.HandlerFunc, 0, 3)
		// 图片端点除外：serveImage 自己带 public/max-age 与 ETag，挂 noStore 会把
		// 海报墙的缓存打掉（每张图全量重下）；其余 API 一律禁止缓存。
		// imageInfo（/Items/:id/Images 结尾、无斜杠）不在排除之列，走 noStore。
		if !strings.Contains(route.path, "/Images/") {
			handlers = append(handlers, noStore)
		}
		if route.auth {
			handlers = append(handlers, a.requireAuth)
		}
		handlers = append(handlers, route.handler)
		g.Handle(route.method, route.path, handlers...)
		if lower := strings.ToLower(route.path); lower != route.path {
			g.Handle(route.method, lower, handlers...)
		}
	}
	a.registerStreamRoutes(g)
	a.registerProxyRoutes(g)
}

// registerProxyRoutes 网页播放器代理端点（透传 Range），同样注册大小写与可选扩展名。
// 与其余 Emby 端点一致挂 requireAuth：这两个端点会把上游直链（常带时效签名）
// 直接回给调用方，或由服务端代拉全量字节流，不能匿名开放。
func (a *App) registerProxyRoutes(g *gin.RouterGroup) {
	for _, base := range []string{"/Videos/:id/proxy", "/videos/:id/proxy"} {
		g.GET(base, noStore, a.requireAuth, a.proxyStream)
		g.HEAD(base, noStore, a.requireAuth, a.proxyStream)
		g.GET(base+".:ext", noStore, a.requireAuth, a.proxyStream)
		g.HEAD(base+".:ext", noStore, a.requireAuth, a.proxyStream)
	}
}

// registerStreamRoutes 同时注册 /Videos 与 /videos 大小写，吞掉可选扩展名。
// 鉴权同 registerProxyRoutes：302 的 Location 就是上游直链，匿名可访问等于凭据外泄。
func (a *App) registerStreamRoutes(g *gin.RouterGroup) {
	for _, base := range []string{"/Videos/:id/stream", "/videos/:id/stream"} {
		g.GET(base, noStore, a.requireAuth, a.stream)
		g.HEAD(base, noStore, a.requireAuth, a.stream)
		g.GET(base+".:ext", noStore, a.requireAuth, a.stream)
		g.HEAD(base+".:ext", noStore, a.requireAuth, a.stream)
	}
}

// scannerPathHints 自动化扫描器常用的路径片段。公网上的 .svn/.git、备份包、
// 配置文件探测会以极高频率命中 NoRoute；不拦下来会把 api_probe 的 1000 条
// 上限刷满，真正需要补的客户端端点反而在列表里看不见。
var scannerPathHints = []string{
	"wp-admin", "wp-login", "wp-content", "wp-includes", "wordpress",
	"phpmyadmin", "phpinfo", "xmlrpc", "cgi-bin", "vendor/phpunit", "webshell",
}

// isScannerProbe 判断 NoRoute 命中的路径是否明显来自自动化扫描器。
//
// 判据只取「Emby 兼容客户端绝不会发」的形状，宁可漏拦也不误伤真实端点：
//   - 路径任一段以 '.' 开头（长度 > 1）：隐藏文件/目录，如 /.svn/entries、
//     /old/.svn/entries、/.git-credentials、/.env；
//   - 末段含 '.'：带扩展名的文件探测，如 /secrets.yml、/emby-115-cf.7z、
//     /web.config、/database.yaml；
//   - 命中常见扫描词。
//
// Emby API 的路径都是无扩展名的驼峰段（/Users/1/Views、/Branding/Configuration、
// /Persons），三条判据都不会命中；而 /Videos/:id/stream.mp4 这类带扩展名的
// 是已注册路由，根本不走 NoRoute。
func isScannerProbe(path string) bool {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return false
	}
	segments := strings.Split(trimmed, "/")
	if strings.Contains(segments[len(segments)-1], ".") {
		return true
	}
	for _, seg := range segments {
		if len(seg) > 1 && seg[0] == '.' {
			return true
		}
	}
	lower := strings.ToLower(trimmed)
	for _, hint := range scannerPathHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// 诊断去重与写库预算参数（BE-08）：128 条内存上限、6 小时去重窗口、
// 令牌桶 60 次突发 + 平均每秒 1 次落库，避免唯一路径洪水把写放大成
// 每秒数百次 SQLite 事务。
const (
	probeSeenLimit      = 128
	probeSeenTTL        = 6 * time.Hour
	probeWriteBurst     = 60.0
	probeWritePerSecond = 1.0
)

type probeSeenEntry struct {
	seenAt time.Time
}

func (a *App) noRoute(c *gin.Context) {
	// 客户端会探测式地反复请求未实现端点；404 一旦被浏览器/中间层缓存，
	// 后续补上实现客户端也拿不到——明确禁止缓存。
	c.Header("Cache-Control", "no-store")
	path := c.Request.URL.Path
	if !strings.HasPrefix(path, "/api/") && !isScannerProbe(path) {
		// Unknown endpoint segments/query keys/body can contain arbitrary secrets.
		// Aggregate by fixed resource family and standard method; never read a body.
		method, safePath := diagnosticMethod(c.Request.Method), unmatchedDiagnosticPath(path)
		key := method + " " + safePath
		write, epoch := a.observeProbe(key, a.probeClock())
		if write {
			a.persistDiagnosticProbe(method, safePath, redactedRequestQuery(c.Request.URL.RawQuery), epoch)
		}
	}
	c.JSON(404, gin.H{"error": "not found", "code": "NOT_FOUND"})
}

func (a *App) probeClock() time.Time {
	if a.probeNow != nil {
		return a.probeNow()
	}
	return time.Now()
}

// observeProbe 在 probeMu 内完成去重、TTL 过期、容量淘汰与写库预算扣减。
// 返回是否应落库，以及预约时的 epoch（clear 提交后 epoch 变化则放弃写入）。
// 预算耗尽时不占位：后续请求会在令牌恢复后重试，避免把端点永久标记为已见。
func (a *App) observeProbe(key string, now time.Time) (bool, uint64) {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	if entry, ok := a.probeSeen[key]; ok {
		if now.Sub(entry.seenAt) < probeSeenTTL {
			return false, a.probeEpoch
		}
		delete(a.probeSeen, key)
	}
	if len(a.probeSeen) >= probeSeenLimit {
		a.evictProbeSeenLocked(now)
	}
	if !a.takeProbeWriteTokenLocked(now) {
		return false, a.probeEpoch
	}
	a.probeSeen[key] = probeSeenEntry{seenAt: now}
	return true, a.probeEpoch
}

// evictProbeSeenLocked 先清过期项，仍满则淘汰最久未出现的键（LRU）。
// 旧实现容量满后把所有新端点都当已见，导致新端点永远进不了诊断列表。
func (a *App) evictProbeSeenLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for key, entry := range a.probeSeen {
		if now.Sub(entry.seenAt) >= probeSeenTTL {
			delete(a.probeSeen, key)
			continue
		}
		if oldestKey == "" || entry.seenAt.Before(oldest) {
			oldestKey, oldest = key, entry.seenAt
		}
	}
	if len(a.probeSeen) >= probeSeenLimit && oldestKey != "" {
		delete(a.probeSeen, oldestKey)
	}
}

func (a *App) takeProbeWriteTokenLocked(now time.Time) bool {
	if a.probeRefillAt.IsZero() {
		a.probeTokens, a.probeRefillAt = probeWriteBurst, now
	}
	if elapsed := now.Sub(a.probeRefillAt); elapsed > 0 {
		a.probeTokens += elapsed.Seconds() * probeWritePerSecond
		if a.probeTokens > probeWriteBurst {
			a.probeTokens = probeWriteBurst
		}
		a.probeRefillAt = now
	}
	if a.probeTokens < 1 {
		return false
	}
	a.probeTokens--
	return true
}
