package main

import (
	"log/slog"
	"net/http"
	"os"

	"emby-go/internal/config"
	"emby-go/internal/server"
)

// version 由发布构建通过 -ldflags "-X main.version=..." 注入，默认 dev。
var version = "dev"

func main() {
	configPath := os.Getenv("EMBY_CONFIG")
	if configPath == "" {
		configPath = "config.yaml"
	}
	cfg, err := config.Load(configPath)
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
	slog.Info("Emby-go listening", "addr", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, app.Handler()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
