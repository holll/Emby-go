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

var monitorBoundsMS = [...]int64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000}

type monitorBucket struct {
	minute                      int64
	initialized                 bool
	total, errors4xx, errors5xx uint64
	histogram                   [len(monitorBoundsMS) + 1]uint64
}
type requestMonitor struct {
	mu         sync.Mutex
	started    time.Time
	instanceID string
	buckets    [monitorRetention]monitorBucket
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
	m.mu.Lock()
	defer m.mu.Unlock()
	b := &m.buckets[index]
	if b.initialized && b.minute > minute {
		return
	} // Do not evict newer data on clock rollback.
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
	bucket := len(monitorBoundsMS)
	for i, upper := range monitorBoundsMS {
		if elapsed <= time.Duration(upper)*time.Millisecond {
			bucket = i
			break
		}
	}
	b.histogram[bucket]++
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for minute := first; minute <= current; minute++ {
		point := monitorPoint{Start: time.Unix(minute*60, 0).UTC().Format(time.RFC3339), Observed: minute >= m.started.Unix()/60 && !now.Before(m.started)}
		if point.Observed {
			var b monitorBucket
			index := minute % monitorRetention
			if index >= 0 && m.buckets[index].initialized && m.buckets[index].minute == minute {
				b = m.buckets[index]
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
