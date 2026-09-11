// Package logging 提供按天切分的日志 writer：控制台 + log/ 目录下文件双写。
//
// 程序运行日志（slog）与网络请求日志（Gin）各占一个文件，文件名形如
// app-2026-09-11.log / request-2026-09-11.log；跨天自动切换到新文件。
// 目录或文件不可写时降级为仅控制台输出并告警，不阻断服务启动与运行。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Dir 日志目录，固定为 log/（相对进程工作目录，与 db 文件同级）。
const Dir = "log"

// dailyLayout 文件名中的日期部分。
const dailyLayout = "2006-01-02"

// DailyWriter 把写入内容同时送往控制台与 <dir>/<prefix>-YYYY-MM-DD.log。
// 零值不可用，请用 NewDailyWriter 构造。
type DailyWriter struct {
	mu      sync.Mutex
	dir     string
	prefix  string
	console io.Writer
	now     func() time.Time

	day    string // 当前已打开文件对应的日期
	file   *os.File
	failed string // 最近一次失败的日期：同一天内不再重试，避免反复告警刷屏
}

// NewDailyWriter 创建双写 writer；console 为控制台出口，一般为 os.Stdout 或 os.Stderr。
func NewDailyWriter(dir, prefix string, console io.Writer) *DailyWriter {
	return &DailyWriter{dir: dir, prefix: prefix, console: console, now: time.Now}
}

// Write 先写控制台、再写文件。控制台始终写；文件出错只降级告警，
// 既不中断控制台输出，也不把错误抛回调用方（日志问题不应影响业务流程）。
func (w *DailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.console.Write(p)
	w.writeFile(p)
	return len(p), nil
}

func (w *DailyWriter) writeFile(p []byte) {
	day := w.now().Format(dailyLayout)
	if w.failed == day {
		return
	}
	if w.file == nil || w.day != day {
		if err := w.open(day); err != nil {
			w.failed = day
			fmt.Fprintf(w.console, "[warn] 日志文件不可写，已降级为仅控制台输出: %v\n", err)
			return
		}
	}
	if _, err := w.file.Write(p); err != nil {
		_ = w.file.Close()
		w.file = nil
		w.failed = day
		fmt.Fprintf(w.console, "[warn] 日志文件写入失败，已降级为仅控制台输出: %v\n", err)
	}
}

// open 打开（不存在则创建）当天日志文件，并关闭前一天的文件。
func (w *DailyWriter) open(day string) error {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, w.prefix+"-"+day+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if w.file != nil {
		_ = w.file.Close()
	}
	w.file, w.day = f, day
	return nil
}

// Close 关闭当前文件，进程退出前调用。
func (w *DailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Loggers 持有进程级两个日志出口。
type Loggers struct {
	app     *DailyWriter
	request *DailyWriter
}

var current *Loggers

// Setup 初始化日志：slog 默认处理器改为「程序日志双写」。
// debug 为真时 slog 级别降到 Debug（复用现有 debug 开关，不新增配置项）。
func Setup(debug bool) *Loggers {
	app := NewDailyWriter(Dir, "app", os.Stderr)
	request := NewDailyWriter(Dir, "request", os.Stdout)
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(app, &slog.HandlerOptions{Level: level})))
	current = &Loggers{app: app, request: request}
	return current
}

// AppWriter 返回程序运行日志出口；未调用 Setup 时退化为 stderr。
func AppWriter() io.Writer {
	if current != nil {
		return current.app
	}
	return os.Stderr
}

// RequestWriter 返回网络请求日志出口；未调用 Setup 时退化为 stdout
// （测试直接构造 App 不经过 Setup，此时行为与改造前一致）。
func RequestWriter() io.Writer {
	if current != nil {
		return current.request
	}
	return os.Stdout
}

// Close 关闭两个日志文件。
func (l *Loggers) Close() error {
	if l == nil {
		return nil
	}
	err := l.app.Close()
	if rerr := l.request.Close(); err == nil {
		err = rerr
	}
	return err
}
