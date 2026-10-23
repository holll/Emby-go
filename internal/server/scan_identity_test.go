package server

import (
	"emby-go/internal/scanner"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"github.com/gin-gonic/gin"
)

// TestScanProgressRunIdentity 覆盖 R2 后端字段：每次运行有 instance_id/run_id，
// 异步入口绑定 task_id，终态保留身份；重启后 instance_id 改变。
func TestScanProgressRunIdentity(t *testing.T) {
	root := t.TempDir()
	a, err := newApp(config.Config{DBPath: filepath.Join(root, "a.db")}, cache.NewMemory(8))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.beginScan(1, 42, scanner.ScanModeFull) {
		t.Fatal("beginScan rejected")
	}
	first := a.scanStatus
	if first.InstanceID == "" || first.RunID == "" || first.TaskID != 42 || !first.Running {
		t.Fatalf("unexpected running identity: %+v", first)
	}
	a.endScan(nil)
	done := a.scanStatus
	if done.Running || done.RunID != first.RunID || done.TaskID != 42 || done.FinishedAt == "" {
		t.Fatalf("terminal state lost identity: %+v", done)
	}
	if !a.beginScan(1, 0, scanner.ScanModeFull) {
		t.Fatal("second beginScan rejected")
	}
	if a.scanStatus.RunID == first.RunID {
		t.Fatalf("run_id reused: %q", a.scanStatus.RunID)
	}
	if a.scanStatus.TaskID != 0 {
		t.Fatalf("task_id should be omitted: %+v", a.scanStatus)
	}
	a.endScan(nil)

	// JSON 契约：终态仍带 instance_id/run_id，task_id 为 0 时省略。
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/scan/progress", nil)
	a.adminScanProgress(c)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["instance_id"] == "" || body["run_id"] == "" || body["running"] != false {
		t.Fatalf("bad progress json: %s", rec.Body.String())
	}
	if _, ok := body["task_id"]; ok {
		t.Fatalf("task_id must be omitted when absent: %s", rec.Body.String())
	}

	b, err := newApp(config.Config{DBPath: filepath.Join(root, "b.db")}, cache.NewMemory(8))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.instanceID == a.instanceID {
		t.Fatal("instance_id must change across restarts")
	}
}
