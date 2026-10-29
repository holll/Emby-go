package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// TestAPIContractCriticalRoutesRegistered 冻结 Emby 浏览主链路与管理 API 的关键路径。
// 删除/改路径会让 v2 的“只增不改”承诺失效，必须同批更新 API-CONTRACT.md。
func TestAPIContractCriticalRoutesRegistered(t *testing.T) {
	a := contractTestApp(t)
	registered := make(map[string]bool, len(a.router.Routes()))
	for _, r := range a.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	critical := []string{
		// Emby 兼容端点（根路径）
		"POST /Users/AuthenticateByName",
		"GET /System/Info/Public",
		"GET /System/Ping",
		"GET /Users/Me",
		"GET /Users/Public",
		"GET /Users/:uid/Views",
		"GET /Users/:uid/Items",
		"GET /Users/:uid/Items/Resume",
		"GET /Items",
		"GET /Items/:id",
		"GET /Items/:id/Images/:kind",
		"GET /Items/:id/Images/:kind/:index",
		"GET /Items/:id/PlaybackInfo",
		"POST /Items/:id/PlaybackInfo",
		"POST /Sessions/Playing",
		"POST /Sessions/Playing/Progress",
		"POST /Sessions/Playing/Stopped",
		"POST /Users/:uid/PlayedItems/:id",
		"DELETE /Users/:uid/PlayedItems/:id",
		"POST /Users/:uid/FavoriteItems/:id",
		"DELETE /Users/:uid/FavoriteItems/:id",
		"POST /Users/:uid/Items/:id/Rating",
		"GET /videos/:id/stream",
		"GET /Videos/:id/proxy",
		// /emby 前缀必须同样可用
		"POST /emby/Users/AuthenticateByName",
		"GET /emby/Users/:uid/Views",
		"GET /emby/Items/:id/Images/:kind",
		// 管理与健康检查
		"GET /api/health",
		"GET /api/auth/status",
		"POST /api/auth/initialize",
		"GET /api/admin/settings",
		"GET /api/admin/dashboard",
		"GET /api/admin/security/auth-limits",
		"GET /api/admin/security/ip-blocklist",
		"POST /api/admin/security/ip-blocklist",
		"DELETE /api/admin/security/ip-blocklist",
		"POST /api/admin/security/unlock",
		"POST /api/admin/apikeys/:key/rotate",
		"GET /api/admin/logs/disk",
		"GET /api/admin/webhook/targets",
		"PUT /api/admin/webhook/targets",
		"GET /api/admin/items",
		"GET /api/admin/tasks/:id",
		"POST /api/admin/tasks/scan",
		"POST /api/admin/translate/run",
		"GET /api/admin/translate/status",
		"POST /api/admin/translate/cancel",
		"GET /Items/:id/ThumbnailSet",
		"GET /Items/:id/Images/Thumbnail",
		"GET /Videos/:id/index.bif",
		"HEAD /Videos/:id/index.bif",
		"GET /api/items/:id/sprites",
		"GET /api/items/:id/sprites/sheets/:page",
		"PUT /api/admin/libraries/:id/sprites",
		"POST /api/admin/sprites/run",
		"GET /api/admin/sprites/status",
		"POST /api/admin/sprites/cancel",
		"GET /api/admin/sprites/capabilities",
		"POST /api/admin/artwork/run",
		"GET /api/admin/artwork/status",
		"POST /api/admin/artwork/cancel",
		"GET /api/admin/artwork/capabilities",
		"GET /api/admin/artwork/candidates",
	}
	for _, key := range critical {
		if !registered[key] {
			t.Errorf("契约路由缺失：%s", key)
		}
	}
}

// TestAPIContractErrorEnvelope 冻结错误信封：401/404/413 必须是含稳定 code 的 JSON。
func TestAPIContractErrorEnvelope(t *testing.T) {
	a := contractTestApp(t)
	cases := []struct {
		name       string
		method     string
		path       string
		body       []byte
		wantStatus int
		wantCode   string
	}{
		{"admin 未认证", http.MethodGet, "/api/admin/settings", nil, http.StatusUnauthorized, "AUTH_INVALID"},
		{"emby 未认证", http.MethodGet, "/Users/Me", nil, http.StatusUnauthorized, "AUTH_INVALID"},
		{"未知路径", http.MethodGet, "/api/definitely-not-here", nil, http.StatusNotFound, "NOT_FOUND"},
		{"请求体超限", http.MethodPost, "/api/auth/initialize", bytes.Repeat([]byte("x"), int(defaultRequestBodyLimit)+1), http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE"},
		{"初始化参数不合法", http.MethodPost, "/api/auth/initialize", []byte(`{"Username":"ab","Pw":"short"}`), http.StatusBadRequest, "INVALID_REQUEST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			rec := httptest.NewRecorder()
			a.router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type=%q，错误响应必须是 JSON", ct)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" && tc.name != "请求体超限" {
				t.Errorf("Cache-Control=%q，错误响应必须 no-store", cc)
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("错误响应不是 JSON：%v body=%s", err, rec.Body.String())
			}
			if body.Error == "" {
				t.Errorf("error 字段为空：%s", rec.Body.String())
			}
			if body.Code != tc.wantCode {
				t.Errorf("code=%q want=%q body=%s", body.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func contractTestApp(t *testing.T) *App {
	t.Helper()
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "contract.db")}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}
