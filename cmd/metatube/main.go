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
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/logging"
	"emby-go/internal/server"
)

// shutdownBudget 是 D09 批准的总退出预算；部署强杀宽限建议 30s，需大于它。
const shutdownBudget = 15 * time.Second

// 网络与磁盘都很快时，分配（JSON 编码、SQL 行扫描）是主要成本，GC 会跟着吃掉
// 一两成 CPU。2 核实测（BenchmarkSearchAdminWidePage200，-count=5 中位数）：
//
//	GOGC=100 → 3.38 ms/op，2 秒内 104 次 GC
//	GOGC=200 → 3.16 ms/op（-6.5%），45 次
//	GOGC=400 → 3.05 ms/op（-10%），23 次
//	GOGC=off → 8.8 ms/op（堆只涨不收，反而慢一倍）
//
// 默认只抬到 200：堆目标翻倍、GC 次数减半，是这台机器上收益/内存的最佳折中；
// 再往上（400）只多 3.5% 吞吐却要再翻一倍堆，不值得做默认值。
const defaultGOGC = 200

// runtimeTuning 是从环境变量解析出的运行时 GC 参数。
type runtimeTuning struct {
	// gcPercent 是 debug.SetGCPercent 的参数：正数是堆增长百分比，-1 关闭 GC。
	gcPercent int
	// memLimit 是 debug.SetMemoryLimit 的软上限字节数，0 表示保持运行时默认（不限）。
	memLimit int64
}

// runtimeTuningFromEnv 解析 EMBY_GOGC / EMBY_GOMEMLIMIT。
// 非法值不阻塞启动（媒体服务器因一个可选调优变量写错而起不来更糟），
// 回落到默认值并在返回的警告里说明。
func runtimeTuningFromEnv(getenv func(string) string) (runtimeTuning, []string) {
	tuning := runtimeTuning{gcPercent: defaultGOGC}
	var warns []string
	if raw := strings.TrimSpace(getenv("EMBY_GOGC")); raw != "" {
		switch strings.ToLower(raw) {
		case "off", "none", "-1":
			tuning.gcPercent = -1
		case "0":
			// GOGC=0 在运行时里等同于关闭 GC；显式写出来也算关闭，不要悄悄变成 200。
			tuning.gcPercent = -1
		default:
			percent, err := strconv.Atoi(raw)
			if err != nil || percent < 0 {
				warns = append(warns, fmt.Sprintf("EMBY_GOGC=%q 无法解析，沿用默认 %d", raw, defaultGOGC))
			} else {
				tuning.gcPercent = percent
			}
		}
	}
	if raw := strings.TrimSpace(getenv("EMBY_GOMEMLIMIT")); raw != "" {
		switch strings.ToLower(raw) {
		case "off", "max", "-1":
			tuning.memLimit = 0
		default:
			limit, err := parseByteSize(raw)
			if err != nil {
				warns = append(warns, fmt.Sprintf("EMBY_GOMEMLIMIT=%q 无法解析，不设内存软上限", raw))
			} else {
				tuning.memLimit = limit
			}
		}
	}
	return tuning, warns
}

// parseByteSize 解析 "1073741824"、"512MiB"、"1GiB" 这类字节数，十进制与二进制
// 后缀都接受（KB/MB/GB 按 1000 计，KiB/MiB/GiB 按 1024 计），与部署脚本里常见的
// 写法保持一致。
func parseByteSize(raw string) (int64, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, errors.New("空值")
	}
	multiplier := int64(1)
	lower := strings.ToLower(text)
	for _, unit := range []struct {
		suffix string
		scale  int64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"kb", 1000}, {"mb", 1000 * 1000}, {"gb", 1000 * 1000 * 1000},
		{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30},
	} {
		if strings.HasSuffix(lower, unit.suffix) {
			multiplier = unit.scale
			text = strings.TrimSpace(text[:len(text)-len(unit.suffix)])
			break
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("必须为正数：%s", raw)
	}
	return value * multiplier, nil
}

// applyRuntimeTuning 把解析结果落到运行时；返回是否真的改了什么，便于日志区分
// 「默认值」与「运维显式设置」。
func applyRuntimeTuning(tuning runtimeTuning) {
	debug.SetGCPercent(tuning.gcPercent)
	if tuning.memLimit > 0 {
		debug.SetMemoryLimit(tuning.memLimit)
	}
}

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

	// GC 调优必须在第一个请求之前做：日志先于它初始化，这样警告能落盘。
	tuning, tuningWarns := runtimeTuningFromEnv(os.Getenv)
	for _, warn := range tuningWarns {
		slog.Warn("运行时调优", "detail", warn)
	}
	applyRuntimeTuning(tuning)
	slog.Info("运行时 GC 参数", "gogc", tuning.gcPercent, "gomemlimit", tuning.memLimit)

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
