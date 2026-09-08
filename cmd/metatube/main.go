package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"emby-go/internal/config"
	"emby-go/internal/server"
)

// version 由发布构建通过 -ldflags "-X main.version=..." 注入，默认 dev。
var version = "dev"

func main() {
	var (
		configPath = flag.String("c", "", "配置文件路径（默认 config.yaml）")
		showVer    = flag.Bool("version", false, "打印版本号后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("Emby-go %s\n", version)
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

	app, err := server.New(cfg)
	if err != nil {
		slog.Error("create server", "error", err)
		os.Exit(1)
	}
	defer app.Close()
	slog.Info("Emby-go listening", "addr", cfg.Addr(), "version", version, "config", path)
	if err := http.ListenAndServe(cfg.Addr(), app.Handler()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
