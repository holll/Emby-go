package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"emby-go/internal/config"
)

// TestAdminMetricsEndpoint 验证指标接口需要管理员鉴权并返回关键字段。
func TestAdminMetricsEndpoint(t *testing.T) {
	a, err := New(config.Config{DBPath: filepath.Join(t.TempDir(), "metrics.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.db.InitializeAdministrator("admin", "password-1234"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.AddLibrary("Metrics Library", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	token := loginForToken(t, a)

	// 未认证必须 401，且管理面错误信封带 code。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/metrics", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证 status=%d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/metrics", nil)
	req.Header.Set("X-Emby-Token", token)
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		UptimeSeconds int64  `json:"uptime_seconds"`
		Goroutines    int    `json:"goroutines"`
		HeapAlloc     uint64 `json:"heap_alloc"`
		Movies        int    `json:"movies"`
		Users         int    `json:"users"`
		Libraries     int    `json:"libraries"`
		CacheBackend  string `json:"cache_backend"`
		Readiness     bool   `json:"readiness"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Goroutines < 1 || body.HeapAlloc == 0 || !body.Readiness {
		t.Fatalf("指标异常: %+v", body)
	}
	if body.Users != 1 || body.Libraries != 1 {
		t.Fatalf("users=%d libraries=%d", body.Users, body.Libraries)
	}
	if body.CacheBackend == "" {
		t.Fatal("cache_backend 为空")
	}
}
