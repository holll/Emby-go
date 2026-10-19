package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"emby-go/internal/config"
)

func TestNewUsesMemoryCacheWithoutRedis(t *testing.T) {
	a, err := New(config.Config{DBPath: filepath.Join(t.TempDir(), "mem.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.cacheBackend != "memory" || a.cacheOnline {
		t.Fatalf("backend=%q online=%v", a.cacheBackend, a.cacheOnline)
	}
	if a.cache == nil {
		t.Fatal("cache 为空")
	}
}

func TestNewFallsBackWhenRedisUnreachable(t *testing.T) {
	a, err := New(config.Config{DBPath: filepath.Join(t.TempDir(), "fallback.db"), RedisAddr: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("Redis 不可用不应拒绝启动: %v", err)
	}
	defer a.Close()
	if a.cacheBackend != "memory" || a.cacheOnline {
		t.Fatalf("backend=%q online=%v", a.cacheBackend, a.cacheOnline)
	}
}

func TestAdminSettingsReportMemoryBackend(t *testing.T) {
	a, err := New(config.Config{DBPath: filepath.Join(t.TempDir(), "settings.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.db.InitializeAdministrator("admin", "password-1234"); err != nil {
		t.Fatal(err)
	}
	token := loginForToken(t, a)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Cache       string `json:"cache"`
		RedisOnline bool   `json:"redis_online"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Cache != "memory" || body.RedisOnline {
		t.Fatalf("settings cache=%q redis_online=%v", body.Cache, body.RedisOnline)
	}
}
