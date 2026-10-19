package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/logging"
)

// logsTestApp 建一个带管理员的 app 并返回可直接访问 admin 路由的 token。
func logsTestApp(t *testing.T) (*App, string) {
	t.Helper()
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "logs.db")}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	seedOwner(t, a)
	token := "fixture-admin-token"
	if err := a.db.SaveAccessTokenForUser(token, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return a, token
}

// TestAdminLogsReturnsRecentEntries 验证管理端能读到运行日志且未认证被拒。
func TestAdminLogsReturnsRecentEntries(t *testing.T) {
	a, token := logsTestApp(t)
	logging.SetupTo(io.Discard, false)
	slog.Info("fixture log line", "purpose", "logs-test")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/logs?limit=50", nil)
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	a.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			Time    string `json:"time"`
			Level   string `json:"level"`
			Message string `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range body.Items {
		if strings.Contains(item.Message, "fixture log line") && item.Level != "" && item.Time != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未找到测试日志: %s", w.Body.String())
	}

	anon := httptest.NewRecorder()
	a.router.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/api/admin/logs", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("未认证 status=%d", anon.Code)
	}
}

// TestSelfCheckFFProbeRecordsFailure 验证 ffprobe 路径无效时给出可展示的错误。
func TestSelfCheckFFProbeRecordsFailure(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "probe.db")}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.cfg.FFProbePath = filepath.Join(t.TempDir(), "missing-ffprobe")
	a.selfCheckFFProbe()
	if a.ffprobeError == "" {
		t.Fatal("缺少 ffprobe 时应记录错误")
	}
	if !strings.Contains(a.ffprobeError, "ffprobe") {
		t.Fatalf("错误信息不可读: %s", a.ffprobeError)
	}
}

// TestAdminSettingsExposeFFProbeState 验证设置接口总是给出探测能力字段。
func TestAdminSettingsExposeFFProbeState(t *testing.T) {
	a, token := logsTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	a.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ffprobe_path", "ffprobe_error"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("settings 缺少 %s: %s", key, w.Body.String())
		}
	}
}
