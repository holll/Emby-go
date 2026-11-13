package server

import (
	"emby-go/internal/store"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAdminDiagnosticsAggregatesEvidence(t *testing.T) {
	_, ts, token := contractEnv(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/diagnostics", nil)
	req.Header.Set("X-Emby-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var got struct {
		GeneratedAt string `json:"generated_at"`
		App         struct {
			Version       string `json:"version"`
			StartedAt     string `json:"started_at"`
			UptimeSeconds int64  `json:"uptime_seconds"`
			GoVersion     string `json:"go_version"`
		} `json:"app"`
		Store struct {
			Path          string `json:"path"`
			SizeBytes     int64  `json:"size_bytes"`
			SchemaVersion int    `json:"schema_version"`
			Movies        struct {
				Success int `json:"success"`
			} `json:"movies"`
			Libraries      int `json:"libraries"`
			TaskRecords    int `json:"task_records"`
			ScheduledTasks struct {
				Total   int `json:"total"`
				Enabled int `json:"enabled"`
			} `json:"scheduled_tasks"`
		} `json:"store"`
		Runtime struct {
			ScanRunning bool `json:"scan_running"`
			Probe       struct {
				Running bool `json:"running"`
			} `json:"probe"`
			Scrape struct {
				Running bool `json:"running"`
			} `json:"scrape"`
		} `json:"runtime"`
		Scheduler []map[string]any `json:"scheduler"`
		Webhook   struct {
			Targets int `json:"targets"`
		} `json:"webhook"`
		ImageCache struct {
			Enabled bool   `json:"enabled"`
			Dir     string `json:"dir"`
		} `json:"image_cache"`
		FFprobe struct {
			Path string `json:"path"`
		} `json:"ffprobe"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, got.GeneratedAt); err != nil {
		t.Fatalf("generated_at = %q", got.GeneratedAt)
	}
	if got.App.Version == "" || got.App.StartedAt == "" || got.App.GoVersion == "" {
		t.Fatalf("app evidence incomplete: %+v", got.App)
	}
	if got.Store.Path == "" || got.Store.SizeBytes <= 0 || got.Store.SchemaVersion != store.LatestSchemaVersion {
		t.Fatalf("store evidence incomplete: %+v", got.Store)
	}
	if got.Store.Libraries < 1 || got.Store.TaskRecords < 0 {
		t.Fatalf("unexpected store evidence: %+v", got.Store)
	}
	if got.Store.ScheduledTasks.Total != 0 || len(got.Scheduler) != 0 {
		t.Fatalf("fresh instance should have no scheduled tasks: %+v", got.Store)
	}
	if got.Runtime.ScanRunning || got.Runtime.Probe.Running || got.Runtime.Scrape.Running {
		t.Fatal("fresh instance should report no running jobs")
	}
}

func TestAdminDiagnosticsRejectsNonAdmin(t *testing.T) {
	_, ts, adminToken := contractEnv(t)
	viewer := viewerToken(t, ts, adminToken)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/diagnostics", nil)
	req.Header.Set("X-Emby-Token", viewer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}
