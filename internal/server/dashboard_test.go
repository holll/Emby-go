package server

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDashboardSummarySingleflightStaleAndUnavailable(t *testing.T) {
	app := &App{}
	var calls atomic.Int32
	n := 7
	app.dashboardSummary.load = func() (dashboardMedia, error) {
		calls.Add(1)
		return dashboardMedia{Total: &n, Success: &n, Manual: &n, Pending: &n, Libraries: &n}, nil
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			v := app.dashboardMediaSnapshot()
			if !v.Available || v.Stale || v.Total == nil || *v.Total != 7 {
				t.Errorf("bad concurrent media %+v", v)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cache stampede %d", calls.Load())
	}
	first := app.dashboardMediaSnapshot()
	app.dashboardSummary.mu.Lock()
	app.dashboardSummary.expires = time.Time{}
	app.dashboardSummary.load = func() (dashboardMedia, error) {
		calls.Add(1)
		return dashboardMedia{}, errors.New("password=secret /private/root")
	}
	app.dashboardSummary.mu.Unlock()
	stale := app.dashboardMediaSnapshot()
	if !stale.Available || !stale.Stale || stale.ErrorCode != "SUMMARY_UNAVAILABLE" || stale.Total == nil || *stale.Total != 7 || stale.UpdatedAt == nil || *stale.UpdatedAt != *first.UpdatedAt {
		t.Fatalf("stale %+v", stale)
	}
	app.dashboardMediaSnapshot()
	if calls.Load() != 2 {
		t.Fatal("failed refresh was not cached")
	}
	empty := &App{}
	empty.dashboardSummary.load = app.dashboardSummary.load
	unavailable := empty.dashboardMediaSnapshot()
	if unavailable.Available || !unavailable.Stale || unavailable.Total != nil || unavailable.Libraries != nil || unavailable.UpdatedAt != nil {
		t.Fatalf("invented zero counts %+v", unavailable)
	}
	raw, _ := json.Marshal(unavailable)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private") {
		t.Fatalf("raw error leaked %s", raw)
	}
}

func TestDashboardResponseWindowsAndPrivacy(t *testing.T) {
	app, _, token := asyncScanFixture(t)
	app.taskMu.Lock()
	app.tasks = []task{{ID: 123, Type: "scan", Status: "failed", StartedAt: time.Now().UTC().Format(time.RFC3339), Error: "secret /private/source?api_key=hidden"}, {ID: 124, Type: "/private/type", Status: "raw-secret", StartedAt: "2026-01-01T00:00:00Z"}}
	app.taskMu.Unlock()
	baseline := app.monitor.snapshot(time.Now(), 60).counts.Total // Fixture initialization/login/library are genuine control requests.
	for _, window := range []string{"5m", "15m", "60m"} {
		w := translationCall(app, token, "GET", "/api/admin/dashboard?window="+window, "")
		if w.Code != 200 {
			t.Fatalf("response %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Instance string         `json:"instance_id"`
			Series   []monitorPoint `json:"series"`
			Requests monitorCounts  `json:"requests"`
			Media    dashboardMedia `json:"media"`
			Tasks    dashboardTasks `json:"tasks"`
			Runtime  map[string]any `json:"runtime"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := map[string]int{"5m": 5, "15m": 15, "60m": 60}[window]
		if body.Instance == "" || len(body.Series) != want || !body.Media.Available || body.Media.Total == nil || body.Media.UpdatedAt == nil || body.Runtime["heap_alloc_bytes"] == nil {
			t.Fatalf("incomplete body %s", w.Body.String())
		}
		if body.Requests.Total != baseline {
			t.Fatalf("dashboard counted itself %+v", body.Requests)
		}
		if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "raw-secret") || strings.Contains(w.Body.String(), "api_key") || strings.Contains(w.Body.String(), "source_path") {
			t.Fatalf("sensitive response %s", w.Body.String())
		}
		if len(body.Tasks.Recent) != 2 || body.Tasks.Recent[1].Kind != "other" || body.Tasks.Recent[1].Status != "unknown" {
			t.Fatalf("unsafe task %+v", body.Tasks)
		}
	}
	for _, q := range []string{"window=", "window=1m", "window=60", "window=5m&window=15m", "window=%20"} {
		w := translationCall(app, token, "GET", "/api/admin/dashboard?"+q, "")
		if w.Code != 400 {
			t.Fatalf("invalid %s: %d", q, w.Code)
		}
	}
}

func TestDashboardTasksBoundedAndMediaDatabaseFailure(t *testing.T) {
	app, _, _ := asyncScanFixture(t)
	app.taskMu.Lock()
	for i := range 25 {
		app.tasks = append(app.tasks, task{ID: int64(i), Type: "probe", Status: "running"})
	}
	app.taskMu.Unlock()
	tasks := app.dashboardTaskSnapshot()
	if len(tasks.Recent) != 10 || tasks.Running != 25 {
		t.Fatalf("tasks %+v", tasks)
	}
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	media := app.dashboardMediaSnapshot()
	if media.Available || !media.Stale || media.ErrorCode != "SUMMARY_UNAVAILABLE" || media.Success != nil {
		t.Fatalf("failed DB %+v", media)
	}
}
