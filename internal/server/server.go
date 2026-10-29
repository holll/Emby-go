package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	"emby-go/internal/imagecache"
	"emby-go/internal/imageutil"
	"emby-go/internal/logging"
	"emby-go/internal/metatube"
	"emby-go/internal/scheduler"
	"emby-go/internal/store"
	"emby-go/internal/webhook"
)

// defaultMemoryCacheEntries 是内存兜底缓存容量（条目数）。
const defaultMemoryCacheEntries = 4096

type App struct {
	sprites           spriteState
	cfg               config.Config
	db                *store.Store
	router            *gin.Engine
	cache             cache.Cache
	webhook           *webhook.Dispatcher
	webhookMu         sync.RWMutex // immutable dispatcher/config snapshots; never held during HTTP delivery
	webhookUpdateMu   sync.Mutex   // serialize persistent saves with shutdown
	webhookConfig     config.WebhookConfig
	webhookTargets    []webhookTargetSpec
	webhookMulti      bool
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
	authGuard *authGuard

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
	// imgDisk 是缩略图变体的磁盘缓存：内存 LRU 只留最近几百条，重启即失效，
	// 磁盘把「同一张图的同一个尺寸」的解码/缩放/编码成本降到一次。
	imgDisk *imagecache.Disk
	// thumbMu/thumbFlights 实现同 key 只生成一次（singleflight）：
	// 海报墙里同一张图会被 <img> 和 srcset 同时请求，信号量只能限流不能去重，
	// 后到的请求等锁后复查命中，但更早的并发请求会排出多份相同的 CPU 工作。
	thumbMu      sync.Mutex
	thumbFlights map[string]*thumbFlight
	// thumbGenerate 是生成变体的实际实现，抽成字段以便测试统计调用次数。
	thumbGenerate func(path string, width, height, quality int) ([]byte, error)
	// imageCacheMu 串行化「读取/保存容量上限」与快照，避免设置页并发保存互相覆盖；
	// imageCacheMaxBytes 是当前生效的上限（0 表示不限制），设置页修改后立即生效。
	imageCacheMu       sync.Mutex
	imageCacheMaxBytes int64
	// scrapeSem 限制同时向上游拉取的缩略图数量：刮削预览页一次十几张，
	// 不加限制就是十几路并发上游请求，慢上游会把 goroutine 堆起来。
	scrapeSem chan struct{}

	// 扫描进度：POST /scan 执行期间由进度回调写入，GET /scan/progress 轮询读取。
	// instanceID 每次启动随机生成，runID 只在本实例内递增；前端用二者区分
	// “同一轮运行”与“重启后重新编号的 task_id”，历史终态不再当新通知。
	scanMu     sync.RWMutex
	scanStatus scanStatus
	instanceID string
	scanRunSeq uint64

	// D10：异步扫描任务表（进程内，容量 200，重启不恢复）。
	scanTaskMu    sync.Mutex
	scanTasks     map[int64]*asyncScanTask
	scanTaskOrder []int64
	scanTaskIdem  map[string]scanIdemEntry

	// 媒体信息探测任务状态。probeCtx 挂在 rootCtx 之下：
	// 任务在 goroutine 里跑，必须用常驻 context——用请求 context 会在 handler 返回时被取消。
	probeTaskMu       sync.RWMutex
	probeStatus       probeStatus
	probeProgressEmit sync.Mutex // R6：串行化「计数 + 进度日志」，保证磁盘严格 [1/N]→[2/N]
	probeCtx          context.Context
	probeCancel       context.CancelFunc
	rootCtx           context.Context
	rootCancel        context.CancelFunc
	startupWG         sync.WaitGroup // join startup DB readers before closing the store

	// 未实现端点的探测记录去重：客户端启动期会反复请求同一路径，
	// 只需记下「哪些端点被调用过」，避免每个 404 都写一次库。
	probeMu        sync.Mutex
	probeSeen      map[string]probeSeenEntry
	probeEpoch     uint64
	probeNow       func() time.Time // 测试可注入时钟；生产为 time.Now
	probeTokens    float64          // 诊断写库令牌（burst 上限见 probeWriteBurst）
	probeRefillAt  time.Time
	probePersistMu sync.Mutex // serialize diagnostic commits and successful clears

	ipBlocks         ipBlockState
	monitor          *requestMonitor
	dashboardSummary dashboardSummaryCache
	startAt          time.Time
	ready            atomic.Bool    // readiness：收到退出信号后立即为 false
	shutdownOnce     sync.Once      // BeginShutdown 幂等
	closeOnce        sync.Once      // Close 幂等
	workerGate       workerGate     // 「登记 worker」与「等待 worker 收齐」的互斥门，见 beginWorker
	workerWG         sync.WaitGroup // 探测/刮削/立即执行计划等后台 worker，退出时有界 join
	// 周期性统计信息维护（ANALYZE）。analyzeStats 可替换以便测试观察调用。
	maintenanceOnce sync.Once
	analyzeStats    func() error
	schedMu         sync.Mutex
	schedDone       context.Context // scheduler.Stop 返回的完成信号（BeginShutdown 写入）

	// 计划任务调度器：定义存 DB，保存后 Reload 即时生效。
	sched *scheduler.Scheduler

	// nfoGate 是扫库、探测、刮削、头像、独立翻译和人工编辑共用的 NFO 写入通道：
	// 它们都会「读全文 → 改局部 → 原子写」同一批 NFO，并发会互相覆盖；
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

	translation translationState
	artwork     artworkState
}

// errServerShuttingDown 是关停窗口内拒绝新后台工作的统一错误。
// 它同时是 503 的判据：handler 侧据此区分「服务正在关停」与真正的任务冲突。
var errServerShuttingDown = errors.New("server shutting down")

// workerGate 是「登记新 worker」与「等待 worker 收齐」之间的互斥门。
//
// 为什么需要它：sync.WaitGroup 只允许「计数从 0 变成 1」发生在 Wait 之前。
// 关停窗口里若 handler 仍在 workerWG.Add(1)，而 WaitBackground/Close 正在
// workerWG.Wait()，运行时就会 panic
// 「sync: WaitGroup misuse: Add called concurrently with Wait」；
// 也可能让 Wait 提前返回，随后 db.Close() 与仍在跑的 worker 相撞。
//
// 约定（两把门，方向互补）：
//   - 登记门（本结构）：BeginShutdown 第一步关门；关门后 beginWorker 一律失败。
//   - rootCtx 门：BeginShutdown 末步取消 rootCtx，各任务入口在已持有的状态锁内判它。
//     BeginShutdown 内「关门」早于「取消 rootCtx」，所以 rootCtx 已取消 ⇒ 登记门必已关闭；
//     反过来关门到取消之间还有极短窗口，靠 rootCtx 门兜住（两门都拒绝，不重复放行）。
//
// 只保护 Add/Wait 这一对，不参与任务状态；shuttingDown 单调置位、永不复位。
type workerGate struct {
	mu           sync.Mutex
	shuttingDown bool
}

// beginWorker 登记一个后台 worker，返回释放函数（形如 defer release()）。
// 登记门已关闭时返回 errServerShuttingDown，调用方应回 503 并**不**启动 goroutine。
func (a *App) beginWorker() (func(), error) {
	a.workerGate.mu.Lock()
	defer a.workerGate.mu.Unlock()
	if a.workerGate.shuttingDown {
		return nil, errServerShuttingDown
	}
	a.workerWG.Add(1)
	return a.workerWG.Done, nil
}

// beginWorkerHTTP 是 handler 侧适配层：登记门关闭时直接回 503 并返回 ok=false。
// 关停后各入口因此统一表现为 503，而不是照常进入真实逻辑再失败。
func (a *App) beginWorkerHTTP(c *gin.Context) (release func(), ok bool) {
	release, err := a.beginWorker()
	if err != nil {
		apiError(c, http.StatusServiceUnavailable, err.Error())
		return nil, false
	}
	return release, true
}

// workerGateAPIError 把「关停门/任务冲突」两类拒绝映射成状态码：
// 关停一律 503（与 beginWorkerHTTP 一致），其余按调用方给的具体冲突码回。
func workerGateAPIError(c *gin.Context, err error, conflictStatus int) {
	status := conflictStatus
	if errors.Is(err, errServerShuttingDown) {
		status = http.StatusServiceUnavailable
	}
	apiError(c, status, err.Error())
}

// stopWorkerAdmission 永久关闭登记门。幂等；必须在任何 workerWG.Wait 之前调用。
func (a *App) stopWorkerAdmission() {
	a.workerGate.mu.Lock()
	a.workerGate.shuttingDown = true
	a.workerGate.mu.Unlock()
}

// workerAdmissionOpen 报告登记门是否仍开着（测试与断言用）。
func (a *App) workerAdmissionOpen() bool {
	a.workerGate.mu.Lock()
	defer a.workerGate.mu.Unlock()
	return !a.workerGate.shuttingDown
}

// waitWorkers 关门后等待全部后台 worker 结束（无预算版本，供 Close 使用）。
// 关门之后不再有新的 Add，因此 Wait 期间计数只减不增：
// 关门前已登记的 Add 一定先于 Wait 发生，关门后的登记请求一律被拒 —— 这正是
// WaitGroup 要求的顺序，故不会触发 misuse panic，也不会漏等。
// 前提：worker 必须响应 rootCtx 取消，否则这里会一直阻塞（调用方负责给预算）。
func (a *App) waitWorkers() {
	a.stopWorkerAdmission()
	a.workerWG.Wait()
}

// scanStatus 管理端可轮询的扫描进度快照。
type scanStatus struct {
	InstanceID    string `json:"instance_id"`
	RunID         string `json:"run_id,omitempty"`
	TaskID        int64  `json:"task_id,omitempty"`
	Running       bool   `json:"running"`
	LibraryIndex  int    `json:"library_index"`
	Libraries     int    `json:"libraries"`
	LibraryName   string `json:"library_name"`
	Mode          string `json:"mode,omitempty"`
	Total         int    `json:"total"`
	Done          int    `json:"done"`
	GroupsTotal   int    `json:"groups_total"`
	GroupsDone    int    `json:"groups_done"`
	Unchanged     int    `json:"unchanged_groups"`
	Added         int    `json:"added"`
	Updated       int    `json:"updated"`
	Deleted       int    `json:"deleted"`
	IndexChanged  bool   `json:"index_changed"`
	CleanupDone   bool   `json:"cleanup_performed"`
	Incomplete    bool   `json:"incomplete"`
	Current       string `json:"current"`
	Success       int    `json:"success"`
	Pending       int    `json:"pending"`
	Skipped       int    `json:"skipped"`
	Failed        int    `json:"failed"`
	UnreadableNFO int    `json:"unreadable_nfo,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	Error         string `json:"error,omitempty"`
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
// newInstanceID 标识本次进程启动；配合实例内递增的 run_id，前端不必比较
// 秒级时间戳，也不会在重启后把从 1 开始重新编号的 task_id 当成同一轮运行。
func newInstanceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("t%d", time.Now().UnixNano())
}

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
	targets, targetSpecs, effectiveWebhook, multiTarget, err := loadWebhookTargets(db, cfg.Webhook)
	if err != nil {
		db.Close()
		return nil, err
	}
	notifier, err := webhook.NewDispatcher(targets, webhook.DefaultEventBudget)
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
	blockRules, err := db.IPBlockRules(context.Background())
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load IP block rules: %w", err)
	}
	a := &App{
		cfg: cfg, db: db, cache: cacheStore, webhook: notifier, webhookConfig: effectiveWebhook,
		webhookTargets: targetSpecs, webhookMulti: multiTarget, cacheBackend: cacheBackend, cacheOnline: cacheOnline,
		logRetentionDays: retentionDays, applyLogRetention: logging.SetRetentionDays,
		tags: make(map[string]tagEntry), nfos: make(map[string]nfoCacheEntry),
		probeSeen: make(map[string]probeSeenEntry), probeNow: time.Now,
		startAt:     time.Now(),
		probeTokens: probeWriteBurst, probeRefillAt: time.Now(),
		imgMeta: cache.NewMemory(imageMetaCacheSize), imgThumb: cache.NewMemory(imageThumbCacheSize),
		thumbSem: make(chan struct{}, maxThumbConcurrency), scrapeSem: make(chan struct{}, maxScrapeImageConcurrency),
		thumbFlights: make(map[string]*thumbFlight), thumbGenerate: imageutil.Thumbnail,
		instanceID:   newInstanceID(),
		authGuard:    newAuthGuard(authGuardConfig{IPRequestRate: cfg.Security.Auth.IPRequestRate, IPRequestBurst: cfg.Security.Auth.IPRequestBurst, GlobalRequestRate: cfg.Security.Auth.GlobalRequestRate, GlobalRequestBurst: cfg.Security.Auth.GlobalRequestBurst, BcryptConcurrency: cfg.Security.Auth.BcryptConcurrency, CredentialBodyBytes: int64(cfg.Security.Auth.CredentialBodyBytes), MaxKeys: cfg.Security.Auth.MaxKeys, AccountCooldownMaxSeconds: cfg.Security.Auth.AccountCooldownMaxSeconds}),
		scanTasks:    make(map[int64]*asyncScanTask),
		scanTaskIdem: make(map[string]scanIdemEntry),
	}
	a.ipBlocks.publish(blockRules)
	a.ready.Store(true)
	a.rootCtx, a.rootCancel = context.WithCancel(context.Background())
	// 变体目录：启动时清一次（过期 + 超限），之后由写入侧按十分钟节流触发。
	a.imgDisk = openImageDisk(cfg)
	a.imageCacheMaxBytes = effectiveImageCacheMaxBytes(cfg)
	// 设置页保存过的上限优先于配置文件：配置是部署默认值，页面是运行期决策。
	if saved, ok, err := loadImageCacheMaxBytes(a.db); err != nil {
		slog.Warn("图片缓存设置不可用，沿用配置文件", "error", err)
	} else if ok {
		a.imgDisk.SetMaxBytes(saved)
		if saved != a.imageCacheMaxBytes {
			// 显式说明生效值来自设置页，避免启动日志里的配置文件默认值被误读。
			slog.Info("图片缓存上限来自设置页", "max_bytes", saved, "config_default_bytes", a.imageCacheMaxBytes)
		}
		a.imageCacheMaxBytes = saved
	}
	a.startupWG.Go(func() {
		removed, freed, err := a.imgDisk.Trim()
		if err != nil {
			slog.Debug("缩略图缓存启动清理失败", "error", err)
			return
		}
		if removed > 0 {
			slog.Info("缩略图缓存启动清理完成", "removed", removed, "freed_bytes", freed)
		}
	})
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
	a.monitor = newRequestMonitor(a.startAt)
	middlewares := append([]gin.HandlerFunc{a.monitor.middleware(), securityHeaders()}, requestDiagnostics(cfg.Debug, logging.RequestWriter(), logging.AppWriter(), a.blocklistedIP())...)
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
	a.startupWG.Go(func() {
		a.checkScrapeBackend()
	})
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

// BeginShutdown 让 readiness 立即失败、关闭 worker 登记门、停止新调度、取消可取消的后台任务。
// 幂等，只做一次；真正等待使用 WaitBackground，超时由调用方决定强制退出。
//
// 顺序有约束：先 ready（对外立刻 503）→ 再关登记门（此后 beginWorker 一律失败，
// 保证 Add 不再与 Wait 并发）→ 再停调度/取消任务 → 最后取消 rootCtx。
// 「关登记门」必须早于「取消 rootCtx」：rootCtx 门是各任务入口的后备判据，
// 它一旦生效说明登记门早已关闭，两门不会出现「rootCtx 已取消但仍能登记」的空档。
func (a *App) BeginShutdown() {
	a.shutdownOnce.Do(func() {
		a.ready.Store(false)
		a.stopWorkerAdmission()
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
	return a.joinBackground(ctx)
}

// joinBackground 是 WaitBackground 的实现：关登记门 → 等 worker/启动自检/调度器。
// ctx 带 deadline 时是有界等待（cmd/metatube 的 shutdownBudget），
// ctx 为 Background 时是无界等待（Close 用，前提是 rootCtx 已取消）。
func (a *App) joinBackground(ctx context.Context) error {
	// 关门先于 Wait：否则等待期间新 handler 仍可 Add，正是 misuse panic 的来源。
	a.stopWorkerAdmission()
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

// Close 取消后台任务、等后台工作收齐并关库；幂等，供 defer 与显式退出路径重复调用。
func (a *App) Close() {
	a.closeOnce.Do(func() {
		a.BeginShutdown()
		// P2-15：关库前必须等 worker 收齐，否则 worker 可能撞上已关闭的 DB
		// （此前只等 startupWG，探测/刮削/扫描 worker 全部漏等）。
		//
		// 死锁论证（无界等待为何安全）：
		//  1) 所有 worker 的取消源都是 rootCtx 或其派生 ctx，而 BeginShutdown 已取消
		//     rootCtx，故 waitWorkers 不会无限等一个「必须靠关库才能退出」的 worker；
		//  2) 关停路径本身有预算：cmd/metatube 先在 shutdownBudget（15s）内并发地
		//     WaitBackground + srv.Shutdown，超时走 os.Exit(1) 强制退出、根本到不了这里；
		//     能走到 Close 说明后台工作已收齐或从未启动；
		//  3) Close 自身不持有任何任务锁（probeTaskMu/scrapeTaskMu/scanTaskMu/schedMu 等），
		//     worker 的 defer 只碰任务状态与 DB，不会回调 Close，故不存在环形等待；
		//  4) Close 也在测试的 t.Cleanup 里被调用：那时 worker 要么没启动，要么已随
		//     BeginShutdown 结束，等待立即返回。
		_ = a.joinBackground(context.Background())
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
	r.GET("/web/artwork.js", a.webAsset)
	r.GET("/web/artwork.css", a.webAsset)
	r.GET("/web/sprites.js", a.webAsset)
	r.GET("/web/sprites.css", a.webAsset)
	r.GET("/web/security.js", a.webAsset)
	r.GET("/api/items/:id/sprites", noStore, a.requireAuth, a.webSpriteManifest)
	r.GET("/api/items/:id/sprites/sheets/:page", noStore, a.requireAuth, a.webSpriteSheet)
	r.GET("/web/navigation.js", a.webAsset)
	r.GET("/web/dashboard.js", a.webAsset)
	r.GET("/web/dashboard.css", a.webAsset)
	r.GET("/web/webhook-settings.js", a.webAsset)
	r.GET("/web/log-settings.js", a.webAsset)
	r.GET("/web/image-cache-settings.js", a.webAsset)
	r.GET("/web/style.css", a.webAsset)
	r.GET("/web/vendor/artplayer.min.js", a.webAsset)
	r.GET("/api/health", a.health)
	r.GET("/healthz", a.health)
	r.GET("/api/version", noStore, a.buildInfo)
	r.GET("/api/auth/status", noStore, a.authStatus)
	r.POST("/api/auth/initialize", noStore, a.credentialLimited(a.initialize))
	auth := r.Group("/api/auth", noStore, a.requireAuth)
	auth.POST("/password", a.credentialLimited(a.changePassword))
	auth.POST("/logout", a.logout)
	// 刮削预览用的图片代理刻意不挂鉴权：它要能直接放进 <img src>，而浏览器不会给
	// 图片请求带自定义头（X-Emby-Token），挂鉴权只会让缩略图全部 401。
	// 上游 MetaTube 的图片端点本身也是公开的（Jellyfin 插件同样直接引用），
	// 这里只做「限定 kind + 限定目标服务 + 限时限量」的转发，不放大暴露面。
	r.GET("/api/admin/scrape/image", a.adminScrapeImage)

	// noStore 必须先于 requireAuth：鉴权失败会 Abort，后面的中间件不再执行。
	admin := r.Group("/api/admin", noStore, a.requireAuth, a.requireAdmin, a.auditAdminAction)
	admin.GET("/libraries", a.adminLibraries)
	// R1：认证防护的可观测与受保护应急解锁；解锁需交互会话 + 当前密码再认证。
	admin.GET("/security/auth-limits", a.adminAuthLimits)
	admin.GET("/security/ip-blocklist", a.adminIPBlocklist)
	admin.POST("/security/ip-blocklist", a.adminMutateIPBlocklist)
	admin.DELETE("/security/ip-blocklist", a.adminMutateIPBlocklist)
	admin.POST("/security/unlock", a.credentialLimited(a.adminUnlockAuth))
	admin.POST("/libraries", a.adminAddLibrary)
	admin.PUT("/libraries/:id/sprites", a.adminLibrarySprites)
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
	admin.POST("/apikeys/:key/rotate", a.credentialLimited(a.adminRotateAPIKey))
	admin.DELETE("/apikeys/:key", a.adminDeleteAPIKey)
	// D01：撤销全部登录会话（API 密钥不受影响）。
	admin.DELETE("/access-tokens", a.adminRevokeAllSessions)
	admin.GET("/audit-events", a.adminAuditEvents)
	admin.GET("/status", a.adminStatus)
	admin.GET("/metrics", a.adminMetrics)
	admin.GET("/dashboard", a.adminDashboard)
	admin.GET("/logs", a.adminLogs)
	admin.GET("/logs/disk", a.adminDiskLogs)
	admin.GET("/logs/settings", a.adminLogSettings)
	admin.PUT("/logs/settings", a.adminSaveLogSettings)
	// 图片缓存（缩略图变体目录）：容量上限、当前占用与手动清理。
	admin.GET("/image-cache/settings", a.adminImageCacheSettings)
	admin.PUT("/image-cache/settings", a.adminSaveImageCacheSettings)
	admin.POST("/image-cache/trim", a.adminTrimImageCache)
	admin.GET("/settings", a.adminSettings)
	admin.GET("/webhook/settings", a.adminWebhookSettings)
	admin.PUT("/webhook/settings", a.adminSaveWebhookSettings)
	admin.GET("/webhook/targets", a.adminWebhookTargets)
	admin.PUT("/webhook/targets", a.adminSaveWebhookTargets)
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
	admin.POST("/translate/run", a.adminTranslationRun)
	admin.GET("/translate/status", a.adminTranslationStatus)
	admin.POST("/translate/cancel", a.adminTranslationCancel)
	admin.GET("/sprites/capabilities", a.adminSpriteCapabilities)
	admin.POST("/sprites/run", a.adminSpriteRun)
	admin.GET("/sprites/status", a.adminSpriteStatus)
	admin.POST("/sprites/cancel", a.adminSpriteCancel)
	admin.POST("/artwork/run", a.adminArtworkRun)
	admin.GET("/artwork/status", a.adminArtworkStatus)
	admin.POST("/artwork/cancel", a.adminArtworkCancel)
	admin.GET("/artwork/capabilities", a.adminArtworkCapabilities)
	admin.GET("/artwork/candidates", a.adminArtworkCandidates)
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
		{"POST", "/Users/AuthenticateByName", false, a.credentialLimited(a.authenticate)},
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
		{"GET", "/Items/:id/ThumbnailSet", true, a.spriteThumbnailSet},
		{"GET", "/Items/:id/Images/Thumbnail", true, a.spriteThumbnail},
		{"GET", "/Items/:id/Images/Thumbnail/:index", true, a.spriteThumbnail},
		{"GET", "/Videos/:id/index.bif", true, a.spriteBIF},
		{"HEAD", "/Videos/:id/index.bif", true, a.spriteBIF},
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
		// imageInfo 和受保护的视频时间点预览不在排除之列；鉴权失败也禁止缓存。
		if !strings.Contains(route.path, "/Images/") || strings.Contains(route.path, "/Images/Thumbnail") {
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
		// Keep verified static segments/decimal IDs; hash unknown segments. Never read a body.
		method, safePath := diagnosticMethod(c.Request.Method), safeDiagnosticProbePath(path)
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
