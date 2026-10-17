package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/logging"
	"emby-go/internal/server"
)

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

	// 运行期版本号转发给 server 包，供管理页外壳（左下角）渲染。
	server.BuildVersion = version

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
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
