package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/logging"
	"emby-go/internal/server"
)

// shutdownBudget 是 D09 批准的总退出预算；部署强杀宽限建议 30s，需大于它。
const shutdownBudget = 15 * time.Second

// 以下变量由发布构建通过 -ldflags "-X main.version=..." 注入，默认值为本地构建。
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	var (
		configPath = flag.String("c", "", "配置文件路径（默认 config.yaml）")
		showVer    = flag.Bool("version", false, "打印版本号后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("Emby-go %s (commit %s, built %s)\n", version, commit, buildTime)
		return
	}

	// 配置文件优先级：-c > EMBY_CONFIG > config.yaml；端口在配置文件的 port/listen 中设置。
	path := *configPath
	if path == "" {
		path = os.Getenv("EMBY_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	// 日志落盘：程序日志与请求日志分别按天写入 log/，同时保留控制台输出。
	logs := logging.Setup(cfg.Debug)
	defer logs.Close()

	// 将同一份构建信息用于真实版本 API、管理页和命令行；不改 Emby 能力版本。
	server.BuildVersion = version
	server.BuildCommit = commit
	server.BuildTime = buildTime

	app, err := server.New(cfg)
	if err != nil {
		slog.Error("create server", "error", err)
		os.Exit(1)
	}
	defer app.Close()
	// 显式超时：默认 http.Server 没有读超时，慢速请求头/请求体可以长期占住连接。
	// 不设 WriteTimeout：302 直链与网页代理是长响应，写超时会截断正在播放的流。
	// 优雅退出（信号、Shutdown、readiness）故意留到 BE-04/D09 决策后再做。
	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // 覆盖 32 MiB 图片上传的慢链路
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	slog.Info("Emby-go listening", "addr", cfg.Addr(), "version", version, "config", path)
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
		return
	case <-sigCtx.Done():
	}

	// D09 已批：readiness 立即失败、停止新调度；HTTP drain 与后台任务取消/join
	// 共享同一个 15 秒预算；长流不主动撤销，超时后走有诊断的强制退出路径
	// （不先关 DB 再放任写入）。
	slog.Info("收到退出信号：开始有界关闭", "budget", shutdownBudget.String())
	app.BeginShutdown()
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- srv.Shutdown(shutCtx) }()
	go func() { errs <- app.WaitBackground(shutCtx) }()
	var failed error
	for range 2 {
		if err := <-errs; err != nil && failed == nil {
			failed = err
		}
	}
	if failed != nil {
		slog.Error("有界关闭超时：强制退出（跳过 DB 优雅关闭）", "error", failed)
		os.Exit(1)
	}
	app.Close()
	slog.Info("已退出")
}
