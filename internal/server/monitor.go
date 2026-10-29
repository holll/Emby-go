package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

const monitorRetention = 60

// monitorShards 是计数分片数。record 原先持一把全局锁，实测 1.9× 并发即饱和
// （mutex profile 99.69% 延迟在 Unlock）；分片后同分钟的数据分散到多个槽，
// snapshot 时再按分钟归并求和，对外口径（总量/4xx/5xx/分位）逐项不变。
// 分片数固定，与 GOMAXPROCS 解耦：归并结果与分片数无关，便于测试与复现。
const monitorShards = 8

var monitorBoundsMS = [...]int64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000}

type monitorBucket struct {
	minute                      int64
	initialized                 bool
	total, errors4xx, errors5xx uint64
	histogram                   [len(monitorBoundsMS) + 1]uint64
}

// monitorShard 是一组独立的分钟环 + 锁。每个分片各自维护「当前槽属于哪一分钟」
// 的滚动语义，跨分片由 snapshot 归并。
type monitorShard struct {
	mu      sync.Mutex
	buckets [monitorRetention]monitorBucket
}
type requestMonitor struct {
	started    time.Time
	instanceID string
	shards     [monitorShards]monitorShard
	// minuteTexts 记忆化 series 每点的 RFC3339 时间串（见 monitorMinuteText）。
	// 挂在实例上而不是包级，避免跨测试/跨实例泄漏，也便于同分钟窗口复用。
	minuteTexts monitorMinuteText
}

// monitorMinuteText 缓存「Unix 分钟 → RFC3339 UTC 文本」。series 的时间戳恒为整分钟，
// 同一分钟对应的输出串永不改变，所以缓存条目只会在槽位被别的分钟占用时失效，
// 不需要淘汰策略。槽位取 minute%monitorRetention，与计数环下标一致：60 分钟窗口
// 的 60 个点各占一槽，dashboard 连续取窗（每轮只滑动一分钟）命中 59/60。
// 锁只覆盖这 60 个纯函数条目，与分片锁无嵌套关系。
type monitorMinuteText struct {
	mu    sync.Mutex
	slots [monitorRetention]monitorMinuteEntry
}

type monitorMinuteEntry struct {
	minute int64
	text   string
}

// formatMonitorMinute 是缓存未命中时的唯一 Format 出口；负分钟（1970 之前）也走它，
// 与改造前逐点 Format 完全同一调用，输出逐字节等价。
func formatMonitorMinute(minute int64) string {
	return time.Unix(minute*60, 0).UTC().Format(time.RFC3339)
}

func (t *monitorMinuteText) text(minute int64) string {
	index := minute % monitorRetention
	if index < 0 { // 负下标不进缓存，语义不变，只是不复用。
		return formatMonitorMinute(minute)
	}
	t.mu.Lock()
	e := &t.slots[index]
	// 空串只可能是从未写入的槽（分钟 0 与零值分钟撞号），Format 结果恒非空，用它判空。
	if e.text == "" || e.minute != minute {
		e.minute, e.text = minute, formatMonitorMinute(minute)
	}
	text := e.text
	t.mu.Unlock()
	return text
}

// shardFor 按记录时刻的纳秒低位选分片：record 每个请求只调一次、时刻各不相同，
// 于是并发请求自然摊到不同分片。刻意不用全局自增计数器或 TryLock 探测链选片
// ——两者都要额外碰一条被所有 P 争用的缓存行/TryLock 原子写，2 核实测把分片
// 收益全部吃回去（-cpu 2 反而比单锁慢 20%）。同一纳秒内的请求会落到同一分片，
// 退化成改造前的行为（正确性不受影响，只是没赚到并行度）。
func (m *requestMonitor) shardFor(at time.Time) *monitorShard {
	return &m.shards[int(at.UnixNano())&(monitorShards-1)]
}

// mergeMonitorBucket 把 src 的计数累加进 dst。同一槽出现不同分钟时保留较新的
// 那一分钟（并丢掉旧分钟的残留计数），与单槽实现里「切到新分钟即清零」一致。
func mergeMonitorBucket(dst *monitorBucket, src *monitorBucket) {
	if !src.initialized {
		return
	}
	if !dst.initialized {
		dst.minute, dst.initialized = src.minute, true
	} else if dst.minute != src.minute {
		if src.minute < dst.minute {
			return
		}
		*dst = monitorBucket{minute: src.minute, initialized: true}
	}
	dst.total += src.total
	dst.errors4xx += src.errors4xx
	dst.errors5xx += src.errors5xx
	for i, n := range src.histogram {
		dst.histogram[i] += n
	}
}

// monitorBucketFor 返回 elapsed 落到的直方图下标；超出最后边界时返回溢出槽
// （下标等于边界个数，即直方图最后一个格子）。
func monitorBucketFor(elapsed time.Duration) int {
	bounds := monitorBoundsMS[:] // 切片化：range 数组会整块拷贝，切片只传头
	for i, upper := range bounds {
		if elapsed <= time.Duration(upper)*time.Millisecond {
			return i
		}
	}
	return len(bounds)
}

func newRequestMonitor(started time.Time) *requestMonitor {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:]) // Instance identifier only, not an authentication secret.
	return &requestMonitor{started: started.UTC(), instanceID: fmt.Sprintf("%d-%s", started.UnixNano(), hex.EncodeToString(nonce[:]))}
}

// Only matched control-plane routes. Never retain paths, queries, headers, bodies,
// identities or per-route labels. Exclude media bytes and monitoring self traffic.
func monitorControlPlane(c *gin.Context) bool {
	path := strings.ToLower(c.FullPath())
	if path == "" || path == "/" || path == "/admin" || path == "/web" || strings.HasPrefix(path, "/web/") || strings.Contains(path, "favicon") {
		return false
	}
	if strings.HasPrefix(path, "/emby/") {
		path = strings.TrimPrefix(path, "/emby")
	}
	if strings.Contains(path, "/images/") || strings.Contains(path, "/stream") || strings.Contains(path, "/proxy") || path == "/api/admin/scrape/image" || strings.HasSuffix(path, "/cover") {
		return false
	}
	switch path {
	case "/healthz", "/api/health", "/system/ping", "/sessions/playing/ping", "/api/admin/dashboard", "/api/admin/metrics", "/api/admin/status", "/api/admin/tasks", "/api/admin/tasks/:id", "/api/admin/scan/progress", "/api/admin/probe/media/progress", "/api/admin/scrape/progress", "/api/admin/translate/status":
		return false
	}
	if c.GetHeader("Upgrade") != "" {
		return false
	}
	ct := strings.ToLower(c.Writer.Header().Get("Content-Type"))
	for _, prefix := range []string{"text/event-stream", "image/", "video/", "audio/", "application/octet-stream"} {
		if strings.HasPrefix(ct, prefix) {
			return false
		}
	}
	return true
}

// Must be installed OUTSIDE recovery: observation happens after recovery writes
// 500. If recovery is absent, record an uncaught panic as 500 then rethrow it.
func (m *requestMonitor) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		defer func() {
			p := recover()
			status := c.Writer.Status()
			if p != nil {
				status = http.StatusInternalServerError
			}
			if monitorControlPlane(c) {
				m.record(time.Now(), time.Since(start), status)
			}
			if p != nil {
				panic(p)
			}
		}()
		c.Next()
	}
}
func (m *requestMonitor) record(at time.Time, elapsed time.Duration, status int) {
	minute := at.Unix() / 60
	index := minute % monitorRetention
	if index < 0 {
		return
	}
	bucket := monitorBucketFor(elapsed)
	shard := m.shardFor(at)
	shard.mu.Lock()
	b := &shard.buckets[index]
	if b.initialized && b.minute > minute { // Do not evict newer data on clock rollback.
		shard.mu.Unlock()
		return
	}
	if !b.initialized || b.minute != minute {
		*b = monitorBucket{minute: minute, initialized: true}
	}
	b.total++
	if status >= 400 && status < 500 {
		b.errors4xx++
	}
	if status >= 500 && status < 600 {
		b.errors5xx++
	}
	// 直方图按局部切片写，越界（溢出格/异常值）一律计入最后一格。用无符号 guard
	// 的 if/else 形态而不是「先夹取再写」：后者的下标夹取是赋值，编译器不跟踪，
	// check_bce 仍会留下 IsInBounds；if/else 两个分支的下标都能被证明在界内。
	hist := b.histogram[:]
	if uint(bucket) >= uint(len(hist)) {
		hist[len(hist)-1]++
	} else {
		hist[bucket]++
	}
	shard.mu.Unlock()
}

type monitorCounts struct {
	Total          uint64   `json:"total"`
	Errors4xx      uint64   `json:"errors_4xx"`
	Errors5xx      uint64   `json:"errors_5xx"`
	Rate           *float64 `json:"rate_per_second"`
	P50            *float64 `json:"p50_ms"`
	P95            *float64 `json:"p95_ms"`
	P50Overflow    bool     `json:"p50_overflow"`
	P95Overflow    bool     `json:"p95_overflow"`
	SampledSeconds float64  `json:"sampled_seconds"`
}
type monitorPoint struct {
	Start       string   `json:"start"`
	Observed    bool     `json:"observed"`
	Requests    *uint64  `json:"requests"`
	Errors4xx   *uint64  `json:"errors_4xx"`
	Errors5xx   *uint64  `json:"errors_5xx"`
	P50         *float64 `json:"p50_ms"`
	P95         *float64 `json:"p95_ms"`
	P50Overflow bool     `json:"p50_overflow"`
	P95Overflow bool     `json:"p95_overflow"`
}
type monitorSnapshot struct {
	counts     monitorCounts
	series     []monitorPoint
	start, end time.Time
}

func monitorQuantile(hist [len(monitorBoundsMS) + 1]uint64, total uint64, fraction float64) (*float64, bool) {
	if total == 0 {
		return nil, false
	}
	rank := uint64(math.Ceil(float64(total) * fraction))
	var seen uint64
	for i, n := range hist {
		seen += n
		if seen >= rank {
			if i == len(monitorBoundsMS) {
				return nil, true
			}
			value := float64(monitorBoundsMS[i])
			return &value, false
		}
	}
	return nil, true
}
func (m *requestMonitor) snapshot(now time.Time, minutes int) monitorSnapshot {
	current := now.Unix() / 60
	first := current - int64(minutes) + 1
	out := monitorSnapshot{start: time.Unix(first*60, 0).UTC(), end: now.UTC(), series: make([]monitorPoint, 0, minutes)}
	observedStart := out.start
	if m.started.After(observedStart) {
		observedStart = m.started
	}
	out.counts.SampledSeconds = math.Max(0, now.Sub(observedStart).Seconds())
	var hist [len(monitorBoundsMS) + 1]uint64
	// 先按分钟槽归并所有分片（每分片只加锁一次），再照原逻辑逐分钟取数。
	// 对外口径不变：某分钟的量 = 落在该分钟的所有分片之和。
	var merged [monitorRetention]monitorBucket
	for s := range m.shards {
		shard := &m.shards[s]
		shard.mu.Lock()
		for i := range shard.buckets {
			mergeMonitorBucket(&merged[i], &shard.buckets[i])
		}
		shard.mu.Unlock()
	}
	for minute := first; minute <= current; minute++ {
		point := monitorPoint{Start: m.minuteTexts.text(minute), Observed: minute >= m.started.Unix()/60 && !now.Before(m.started)}
		if point.Observed {
			var b monitorBucket
			index := minute % monitorRetention
			if index >= 0 && merged[index].initialized && merged[index].minute == minute {
				b = merged[index]
			}
			total, e4, e5 := b.total, b.errors4xx, b.errors5xx
			point.Requests, point.Errors4xx, point.Errors5xx = &total, &e4, &e5
			point.P50, point.P50Overflow = monitorQuantile(b.histogram, b.total, .50)
			point.P95, point.P95Overflow = monitorQuantile(b.histogram, b.total, .95)
			out.counts.Total += b.total
			out.counts.Errors4xx += b.errors4xx
			out.counts.Errors5xx += b.errors5xx
			for i, n := range b.histogram {
				hist[i] += n
			}
		}
		out.series = append(out.series, point)
	}
	out.counts.P50, out.counts.P50Overflow = monitorQuantile(hist, out.counts.Total, .50)
	out.counts.P95, out.counts.P95Overflow = monitorQuantile(hist, out.counts.Total, .95)
	if out.counts.SampledSeconds > 0 {
		rate := float64(out.counts.Total) / out.counts.SampledSeconds
		out.counts.Rate = &rate
	}
	return out
}
