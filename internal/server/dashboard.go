package server

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"runtime"
	"sync"
	"time"
)

const dashboardSummaryTTL = 15 * time.Second

type dashboardMedia struct {
	Success   *int    `json:"success"`
	Manual    *int    `json:"manual"`
	Pending   *int    `json:"pending"`
	Total     *int    `json:"total"`
	Libraries *int    `json:"libraries"`
	UpdatedAt *string `json:"updated_at"`
	Available bool    `json:"available"`
	Stale     bool    `json:"stale"`
	ErrorCode string  `json:"error_code"`
}
type dashboardSummaryCache struct {
	mu      sync.Mutex
	expires time.Time
	value   dashboardMedia
	// Test seam; invoked only under mu. No background sampler or write is used.
	load func() (dashboardMedia, error)
}

func (a *App) loadDashboardMedia() (dashboardMedia, error) {
	var counts [3]int
	for i, status := range []string{"success", "manual", "pending"} {
		count, err := a.db.CountByStatus(status)
		if err != nil {
			return dashboardMedia{}, err
		}
		counts[i] = count
	}
	libs, err := a.db.Libraries()
	if err != nil {
		return dashboardMedia{}, err
	}
	total, libraries := counts[0]+counts[1]+counts[2], len(libs)
	return dashboardMedia{Success: &counts[0], Manual: &counts[1], Pending: &counts[2], Total: &total, Libraries: &libraries, Available: true}, nil
}

// A synchronous mutex is the singleflight: concurrent refreshes share either
// one successful sample or one bounded error-cache interval. Stale values never
// acquire a new updated_at and first failures never masquerade as zero counts.
func (a *App) dashboardMediaSnapshot() dashboardMedia {
	cache := &a.dashboardSummary
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if now.Before(cache.expires) {
		return cache.value
	}
	load := cache.load
	if load == nil {
		load = a.loadDashboardMedia
	}
	value, err := load()
	sampled := time.Now().UTC()
	cache.expires = sampled.Add(dashboardSummaryTTL)
	if err != nil {
		cache.value.Stale = true
		cache.value.ErrorCode = "SUMMARY_UNAVAILABLE"
		return cache.value
	}
	stamp := sampled.Format(time.RFC3339)
	value.UpdatedAt = &stamp
	value.Available = true
	cache.value = value
	return value
}

type dashboardTask struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}
type dashboardTasks struct {
	Running int             `json:"running"`
	Recent  []dashboardTask `json:"recent"`
}

func (a *App) dashboardTaskSnapshot() dashboardTasks {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	out := dashboardTasks{Recent: make([]dashboardTask, 0, 10)}
	for _, task := range a.tasks {
		if task.Status == "running" {
			out.Running++
		}
		if len(out.Recent) >= 10 {
			continue
		}
		kind := task.Type
		switch kind {
		case "scan", "probe", "scrape", "scrape_avatars", "translate", "reindex", "analyze", "webhook", "edit":
		default:
			kind = "other"
		}
		status := task.Status
		switch status {
		case "running", "success", "failed", "skipped":
		default:
			status = "unknown"
		}
		out.Recent = append(out.Recent, dashboardTask{ID: task.ID, Kind: kind, Status: status, StartedAt: task.StartedAt, EndedAt: task.EndedAt})
	}
	return out
}
func (a *App) adminDashboard(c *gin.Context) {
	window := 15
	if values, present := c.Request.URL.Query()["window"]; present {
		if len(values) != 1 {
			apiError(c, 400, "invalid dashboard window")
			return
		}
		switch values[0] {
		case "5m":
			window = 5
		case "15m":
			window = 15
		case "60m":
			window = 60
		default:
			apiError(c, 400, "invalid dashboard window")
			return
		}
	}
	// Slow summary refresh is completed before the single timestamp used by both
	// runtime and request windows, so all observed seconds have the same endpoint.
	media := a.dashboardMediaSnapshot()
	now := time.Now().UTC()
	snapshot := a.monitor.snapshot(now, window)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"instance_id": a.monitor.instanceID, "started_at": a.monitor.started.Format(time.RFC3339Nano), "generated_at": now.Format(time.RFC3339Nano),
		"retention_minutes": monitorRetention, "window_minutes": window, "window_start": snapshot.start.Format(time.RFC3339), "window_end": now.Format(time.RFC3339Nano),
		"requests": snapshot.counts, "series": snapshot.series,
		"latency": gin.H{"unit": "ms", "method": "histogram_upper_bound", "max_finite_ms": 60000, "scope": "json_control_plane", "rate_denominator": "seconds_since_max_window_start_or_process_start", "window_alignment": "current_partial_minute_plus_previous_minutes", "sample_assignment": "request_completion_time"},
		"runtime": gin.H{"uptime_seconds": time.Since(a.startAt).Seconds(), "goroutines": runtime.NumGoroutine(), "heap_alloc_bytes": mem.HeapAlloc, "heap_inuse_bytes": mem.HeapInuse, "go_sys_bytes": mem.Sys, "num_gc": mem.NumGC},
		"media":   media, "tasks": a.dashboardTaskSnapshot(),
	})
}
