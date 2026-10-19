package logging

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// RingSize 是 WebUI 运行日志面板保留的最近条数。
const RingSize = 500

// Entry 是运行日志的一条快照，字段名直接作为管理端 JSON 输出。
type Entry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Ring 是固定容量的内存环形日志缓冲；写满后覆盖最旧条目。
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool
}

// NewRing 创建容量为 RingSize 的日志缓冲。
func NewRing() *Ring {
	return &Ring{entries: make([]Entry, RingSize)}
}

var defaultRing = NewRing()

// Logs 返回最近 limit 条日志（按时间正序）；limit<=0 或超过容量时取全部。
func Logs(limit int) []Entry {
	return defaultRing.Snapshot(limit)
}

// Snapshot 返回副本，调用方可以安全序列化。
func (r *Ring) Snapshot(limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := len(r.entries)
	if !r.full {
		size = r.next
	}
	if limit <= 0 || limit > size {
		limit = size
	}
	out := make([]Entry, 0, limit)
	start := size - limit
	for i := 0; i < limit; i++ {
		idx := start + i
		if r.full {
			idx = (r.next + idx) % len(r.entries)
		}
		out = append(out, r.entries[idx])
	}
	return out
}

func (r *Ring) add(entry Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = entry
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
}

// RingWriter 把写入的文本日志按行留存到环形缓冲，供管理端查询。
// 它只是 io.Writer，不参与 slog.Handler 链，避免与标准库 log 的桥接互相调用。
type RingWriter struct {
	ring *Ring
	now  func() time.Time
}

// NewRingWriter 创建默认环形缓冲的写出器。
func NewRingWriter() *RingWriter {
	return &RingWriter{ring: defaultRing, now: time.Now}
}

func (w *RingWriter) Write(p []byte) (int, error) {
	now := w.now
	if now == nil {
		now = time.Now
	}
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry := Entry{Time: now().Format(time.RFC3339), Level: "INFO", Message: redactSecrets(line)}
		for _, field := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(field, "time="):
				entry.Time = strings.Trim(strings.TrimPrefix(field, "time="), `"`)
			case strings.HasPrefix(field, "level="):
				entry.Level = strings.Trim(strings.TrimPrefix(field, "level="), `"`)
			}
		}
		w.ring.add(entry)
	}
	return len(p), nil
}

// SetupTo 把 slog 默认处理器设为「文本输出到 console + 环形缓冲留存」。
// 供 Setup 与测试复用，不写磁盘。
func SetupTo(console io.Writer, debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(console, NewRingWriter()), &slog.HandlerOptions{Level: level})))
}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"password", "token", "secret", "authorization", "api_key", "apikey", "cookie"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// redactSecrets 兜底清理整行文本里可能出现的凭据（key=value 形式）。
func redactSecrets(line string) string {
	fields := strings.Fields(line)
	for i, field := range fields {
		key, _, ok := strings.Cut(field, "=")
		if !ok || !isSensitiveKey(key) {
			continue
		}
		rest := field[len(key):]
		if strings.HasPrefix(rest, "=") {
			fields[i] = key + "=[REDACTED]"
		}
	}
	return strings.Join(fields, " ")
}
