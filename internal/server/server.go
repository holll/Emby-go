package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	adminName  string
	serverID   string
	serverName string
	taskMu     sync.Mutex
	tasks      []task
	nextTaskID int64
}

// New 以 Redis 为必选缓存后端装配服务：Redis 不可用则启动失败。
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
	a := &App{cfg: cfg, db: db, cache: cacheStore}
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
	gin.SetMode(gin.DebugMode)
	r := gin.New()
	r.Use(gin.Logger(), requestContentLogger(), gin.Recovery())
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
	r.GET("/api/auth/status", a.authStatus)
	r.POST("/api/auth/initialize", a.initialize)

	admin := r.Group("/api/admin", a.requireAuth)
	admin.GET("/libraries", a.adminLibraries)
	admin.POST("/libraries", a.adminAddLibrary)
	admin.POST("/scan", a.adminScan)
	admin.POST("/tasks/scan", a.adminScan)
	admin.POST("/reindex", a.adminReindex)
	admin.GET("/items", a.adminItems)
	admin.DELETE("/items/:id", a.adminDelete)
	admin.GET("/probe", a.adminProbe)
	admin.DELETE("/probe", a.adminClearProbes)
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

// registerEmby 注册全部 Emby 兼容路由。注意 Gin 大小写敏感，
// 而 Emby 生态按规范大小写发请求，因此流端点同时注册 /Videos 与 /videos。
func (a *App) registerEmby(g *gin.RouterGroup) {
	g.POST("/Users/AuthenticateByName", a.authenticate)
	g.GET("/Users/Public", a.publicUsers)
	g.GET("/Users/Me", a.requireAuth, a.me)
	g.GET("/Users/:uid/Items/Latest", a.requireAuth, a.latest)
	g.GET("/Users/:uid/Suggestions", a.requireAuth, a.suggestions)
	g.GET("/System/Info/Public", a.publicInfo)
	g.GET("/System/Info", a.requireAuth, a.info)
	g.GET("/System/Configuration", a.requireAuth, a.configuration)
	g.GET("/System/Endpoint", a.requireAuth, a.endpoint)
	g.GET("/System/Ping", a.ping)
	g.POST("/System/Ping", a.ping)
	g.POST("/Sessions/Playing/Ping", a.requireAuth, a.noContent)
	g.POST("/Sessions/Capabilities", a.requireAuth, a.noContent)
	g.POST("/Sessions/Capabilities/Full", a.requireAuth, a.noContent)
	g.POST("/Sessions/Logout", a.requireAuth, a.noContent)
	g.GET("/DisplayPreferences/:pref", a.requireAuth, a.displayPreferences)
	g.POST("/DisplayPreferences/:pref", a.requireAuth, a.displayPreferences)
	g.GET("/System/Ext/ServerDomains", a.requireAuth, a.serverDomains)
	g.GET("/Items/Counts", a.requireAuth, a.counts)
	g.GET("/Search/Hints", a.requireAuth, a.searchHints)
	g.GET("/Genres", a.requireAuth, a.genres)
	g.GET("/Users/:uid/Items/Resume", a.requireAuth, a.resume)
	g.GET("/Shows/NextUp", a.requireAuth, a.nextUp)
	g.GET("/Users/:uid/Views", a.requireAuth, a.views)
	g.GET("/Users/:uid/Items", a.requireAuth, a.items)
	g.GET("/Items", a.requireAuth, a.rootItems)
	g.GET("/Users/:uid/Items/:id", a.requireAuth, a.item)
	g.GET("/Items/:id", a.requireAuth, a.item)
	g.GET("/Items/:id/Images/:kind", a.image)
	g.GET("/Items/:id/Images/:kind/:index", a.image)
	g.GET("/Items/:id/Images", a.requireAuth, a.imageInfo)
	g.GET("/Items/:id/Similar", a.requireAuth, a.similar)
	g.GET("/Items/:id/PlaybackInfo", a.requireAuth, a.playback)
	g.POST("/Items/:id/PlaybackInfo", a.requireAuth, a.playback)
	g.GET("/Videos/:id/AdditionalParts", a.requireAuth, a.additionalParts)
	g.GET("/Episode/:id/IntroSkipperSegments", a.requireAuth, a.emptyList)
	g.GET("/MediaSegments/:id", a.requireAuth, a.emptyList)
	g.POST("/Sessions/Playing", a.requireAuth, a.playing)
	g.POST("/Sessions/Playing/Progress", a.requireAuth, a.playing)
	g.POST("/Sessions/Playing/Stopped", a.requireAuth, a.playing)
	g.POST("/Users/:uid/PlayedItems/:id", a.requireAuth, a.played)
	g.DELETE("/Users/:uid/PlayedItems/:id", a.requireAuth, a.unplayed)
	g.POST("/Users/:uid/FavoriteItems/:id", a.requireAuth, a.favorite)
	g.DELETE("/Users/:uid/FavoriteItems/:id", a.requireAuth, a.unfavorite)
	g.POST("/Users/:uid/FavoriteItems/:id/Delete", a.requireAuth, a.unfavorite)
	g.POST("/Users/:uid/Items/:id/Rating", a.requireAuth, a.rating)
	g.DELETE("/Users/:uid/Items/:id/Rating", a.requireAuth, a.unrate)
	g.POST("/Users/:uid/Items/:id/Rating/Delete", a.requireAuth, a.unrate)
	g.POST("/Users/:uid/Items/:id/HideFromResume", a.requireAuth, a.hideFromResume)
	registerStreamRoutes(g, a.stream)
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
		body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 4096))
		_ = a.db.Probe(c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery, string(body))
	}
	c.JSON(404, gin.H{"error": "not found"})
}

func (a *App) EnsureDir(path string) error { return os.MkdirAll(filepath.Dir(path), 0755) }
