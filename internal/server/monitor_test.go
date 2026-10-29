package server

import (
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestMonitorHistogramAndWindow(t *testing.T) {
	start := time.Date(2026, 10, 25, 0, 0, 30, 0, time.UTC)
	m := newRequestMonitor(start)
	empty := m.snapshot(start, 5)
	if len(empty.series) != 5 || empty.counts.Total != 0 || empty.counts.P95 != nil || empty.counts.Rate != nil {
		t.Fatalf("initial %+v", empty)
	}
	for i, p := range empty.series {
		if i < 4 && (p.Observed || p.Requests != nil || p.P95 != nil) {
			t.Fatalf("invented history %+v", p)
		}
		if i == 4 && (!p.Observed || p.Requests == nil || *p.Requests != 0 || p.P95 != nil) {
			t.Fatalf("current empty %+v", p)
		}
	}
	// 90 observations <=10ms, five <=20ms, five >60s: P95 upper bound
	// is 20ms (not the newest bucket or arithmetic mean).
	for i := range 100 {
		duration := 10 * time.Millisecond
		status := 200
		if i >= 90 {
			duration = 11 * time.Millisecond
			status = 404
		}
		if i >= 95 {
			duration = 61 * time.Second
			status = 500
		}
		m.record(start.Add(time.Second), duration, status)
	}
	snapshot := m.snapshot(start.Add(30*time.Second), 5)
	c := snapshot.counts
	if c.Total != 100 || c.Errors4xx != 5 || c.Errors5xx != 5 || c.P50 == nil || *c.P50 != 10 || c.P95 == nil || *c.P95 != 20 || c.P95Overflow || c.SampledSeconds != 30 || c.Rate == nil || *c.Rate != 100.0/30 {
		t.Fatalf("distribution %+v", c)
	}
	for range 10 {
		m.record(start.Add(2*time.Second), time.Minute+time.Nanosecond, 503)
	}
	overflow := m.snapshot(start.Add(time.Minute), 5)
	if overflow.counts.P95 != nil || !overflow.counts.P95Overflow {
		t.Fatalf("overflow underestimated %+v", overflow.counts)
	}
	old := m.snapshot(start.Add(61*time.Minute), 60)
	if old.counts.Total != 0 || old.counts.P95 != nil {
		t.Fatalf("expired request survived %+v", old.counts)
	}
	m.record(start.Add(60*time.Minute), time.Millisecond, 200)
	m.record(start, time.Millisecond, 200) // Same slot, old observation cannot evict new.
	latest := m.snapshot(start.Add(61*time.Minute), 5)
	if latest.counts.Total != 1 {
		t.Fatalf("ring rollover %+v", latest.counts)
	}
	if len(m.buckets) != 60 {
		t.Fatal("unbounded retention")
	}
}

func TestMonitorMiddlewareRecoveryAndFilters(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "debug"}[debug], func(t *testing.T) {
			m := newRequestMonitor(time.Now())
			r := gin.New()
			r.Use(m.middleware())
			r.Use(requestDiagnostics(debug, io.Discard, io.Discard)...)
			r.GET("/api/value", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
			r.GET("/api/failure", func(c *gin.Context) { panic("do not retain secret") })
			r.GET("/api/missing", func(c *gin.Context) { c.AbortWithStatusJSON(404, gin.H{"error": "missing"}) })
			r.GET("/api/denied", func(c *gin.Context) { c.AbortWithStatusJSON(403, gin.H{"error": "denied"}) })
			excluded := []string{"/api/admin/dashboard", "/api/admin/metrics", "/api/admin/status", "/api/admin/tasks", "/api/admin/tasks/:id", "/api/admin/scan/progress", "/api/health", "/healthz", "/System/Ping", "/Sessions/Playing/Ping", "/web/dashboard.js", "/favicon.ico", "/Items/:id/Images/:kind", "/emby/items/:id/images/:kind", "/Videos/:id/stream", "/emby/videos/:id/proxy.:ext", "/api/admin/scrape/image", "/api/admin/libraries/:id/cover"}
			for _, path := range excluded {
				r.GET(path, func(c *gin.Context) { c.JSON(200, gin.H{}) })
			}
			r.GET("/api/events", func(c *gin.Context) { c.Header("Content-Type", "text/event-stream"); c.Status(200) })
			r.GET("/api/download", func(c *gin.Context) { c.Data(200, "application/octet-stream", []byte("data")) })
			for _, path := range []string{"/api/value?api_key=secret", "/api/failure", "/api/missing", "/api/denied"} {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if path == "/api/failure" && w.Code != 500 {
					t.Fatalf("recovery %d", w.Code)
				}
			}
			for _, path := range append(excluded, "/api/events", "/api/download", "/does-not-exist") {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			}
			c := m.snapshot(time.Now(), 5).counts
			if c.Total != 4 || c.Errors4xx != 2 || c.Errors5xx != 1 {
				t.Fatalf("filter/recovery counts %+v", c)
			}
		})
	}
}

func TestMonitorUncaughtPanicIsRethrown(t *testing.T) {
	m := newRequestMonitor(time.Now())
	r := gin.New()
	r.Use(m.middleware())
	r.GET("/api/panic", func(c *gin.Context) { panic("expected") })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic swallowed")
			}
		}()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/panic", nil))
	}()
	if got := m.snapshot(time.Now(), 5).counts; got.Total != 1 || got.Errors5xx != 1 {
		t.Fatalf("uncaught panic %+v", got)
	}
}

func TestMonitorConcurrentRecordAndSnapshot(t *testing.T) {
	now := time.Now()
	m := newRequestMonitor(now.Add(-time.Minute))
	const workers = 16
	const n = 1000
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range n {
				m.record(now, 2*time.Millisecond, http.StatusOK)
				if j%50 == 0 {
					m.snapshot(now, 60)
				}
			}
		})
	}
	wg.Wait()
	if got := m.snapshot(now, 5).counts.Total; got != workers*n {
		t.Fatalf("lost samples %d", got)
	}
}

func BenchmarkMonitorRecord(b *testing.B) {
	m := newRequestMonitor(time.Now())
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.record(now, 17*time.Millisecond, 200)
		}
	})
}
func BenchmarkMonitorSnapshot(b *testing.B) {
	m := newRequestMonitor(time.Now())
	now := time.Now()
	m.record(now, time.Millisecond, 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.snapshot(now, 60)
	}
}
