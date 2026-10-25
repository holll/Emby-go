package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func imageCacheCall(a *App, method, path, token, tag, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin"+path, strings.NewReader(body))
	r.Header.Set("X-Emby-Token", token)
	r.Header.Set("If-Match", tag)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func imageCacheSnapshotOf(t *testing.T, a *App, token string) imageCacheSettings {
	t.Helper()
	w := imageCacheCall(a, "GET", "/image-cache/settings", token, "", "")
	if w.Code != 200 {
		t.Fatalf("GET settings: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var snap imageCacheSettings
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestImageCacheSettingsAuthorizationValidationAndPersistence(t *testing.T) {
	f := newAdminDeleteFixture(t)
	f.a.startupWG.Wait()
	reader, err := f.a.db.CreateUser("cache-reader", "test-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("cache-reader", reader.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		path := "/image-cache/settings"
		if method == "POST" {
			path = "/image-cache/trim"
		}
		for token, want := range map[string]int{"": 401, "cache-reader": 403} {
			if w := imageCacheCall(f.a, method, path, token, "", ""); w.Code != want {
				t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
			}
		}
	}
	snap := imageCacheSnapshotOf(t, f.a, "fixture-admin-delete")
	if !snap.Enabled || snap.Dir == "" || snap.MaxBytes != int64(2)<<30 || snap.MaxAllowedBytes != imageCacheMaxBytesLimit || snap.ConfigETag != imageCacheETag(snap.MaxBytes) {
		t.Fatalf("snapshot: %+v", snap)
	}
	if !strings.HasSuffix(snap.Dir, "image-cache") {
		t.Fatalf("dir = %q", snap.Dir)
	}
	// 前置条件与请求体校验。
	if w := imageCacheCall(f.a, "PUT", "/image-cache/settings", "fixture-admin-delete", "", `{"max_bytes":1024}`); w.Code != 428 {
		t.Fatalf("missing If-Match: %d %s", w.Code, w.Body.String())
	}
	for _, raw := range []string{`{}`, `null`, `{"max_bytes":null}`, `{"max_bytes":-1}`, `{"max_bytes":true}`, `{"max_bytes":1.5}`, `{"max_bytes":68719476737}`, `{"max_bytes":1024,"extra":1}`, `{"max_bytes":1024} {}`, strings.Repeat(" ", 1025) + `{}`} {
		if w := imageCacheCall(f.a, "PUT", "/image-cache/settings", "fixture-admin-delete", imageCacheETag(snap.MaxBytes), raw); w.Code != 400 {
			t.Fatalf("%s: %d %s", raw, w.Code, w.Body.String())
		}
	}
	if w := imageCacheCall(f.a, "PUT", "/image-cache/settings", "fixture-admin-delete", `"ic1-1"`, `{"max_bytes":1024}`); w.Code != 412 || !strings.Contains(w.Body.String(), "IMAGE_CACHE_SETTINGS_CHANGED") {
		t.Fatalf("stale If-Match: %d %s", w.Code, w.Body.String())
	}
	w := imageCacheCall(f.a, "PUT", "/image-cache/settings", "fixture-admin-delete", imageCacheETag(snap.MaxBytes), `{"max_bytes":1073741824}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var saved imageCacheSettings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.MaxBytes != 1<<30 || saved.ConfigETag != imageCacheETag(1<<30) {
		t.Fatalf("saved: %+v", saved)
	}
	raw, err := f.a.db.Setting(settingImageCacheMaxBytes)
	if err != nil || raw != "1073741824" {
		t.Fatalf("persisted = %q, %v", raw, err)
	}
	// 重启保留：设置页的值覆盖配置文件默认值。
	f.a.Close()
	reopened, err := newApp(f.a.cfg, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := imageCacheSnapshotOf(t, reopened, "fixture-admin-delete"); got.MaxBytes != 1<<30 {
		t.Fatalf("reopened max_bytes = %d", got.MaxBytes)
	}
	// 0 表示不限制，且立即生效到 Disk。
	w = imageCacheCall(reopened, "PUT", "/image-cache/settings", "fixture-admin-delete", imageCacheETag(1<<30), `{"max_bytes":0}`)
	if w.Code != 200 || reopened.imgDisk.MaxBytes() != 0 {
		t.Fatalf("unlimited: %d %s disk=%d", w.Code, w.Body.String(), reopened.imgDisk.MaxBytes())
	}
	if got := imageCacheSnapshotOf(t, reopened, "fixture-admin-delete"); got.MaxBytes != 0 {
		t.Fatalf("max_bytes = %d", got.MaxBytes)
	}
}

func TestImageCacheSettingsUsageAndTrim(t *testing.T) {
	f := newAdminDeleteFixture(t)
	f.a.startupWG.Wait()
	for _, key := range []string{"usage-a", "usage-b", "usage-c"} {
		path := f.a.imgDisk.PathFor(key)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 1000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap := imageCacheSnapshotOf(t, f.a, "fixture-admin-delete")
	if snap.Files != 3 || snap.UsedBytes != 3000 {
		t.Fatalf("usage: %+v", snap)
	}
	// 上限调到 2048：清理到 80% 水位（1638），只剩最新一份。
	w := imageCacheCall(f.a, "PUT", "/image-cache/settings", "fixture-admin-delete", snap.ConfigETag, `{"max_bytes":2048}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var trimmed imageCacheSettings
	if err := json.Unmarshal(w.Body.Bytes(), &trimmed); err != nil {
		t.Fatal(err)
	}
	if trimmed.UsedBytes > 2048 || trimmed.Files > 1 {
		t.Fatalf("after trim: %+v", trimmed)
	}
	w = imageCacheCall(f.a, "POST", "/image-cache/trim", "fixture-admin-delete", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"freed_bytes"`) {
		t.Fatalf("trim: %d %s", w.Code, w.Body.String())
	}
}

func TestImageCacheSettingsDisabledAndCorruptFallback(t *testing.T) {
	a, _, path := authorityApp(t)
	a.Close()
	disabled, err := newApp(config.Config{DBPath: path, ImageCache: config.ImageCacheConfig{Enabled: boolPtr(false)}}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if err := disabled.db.InitializeAdministrator("admin", "fixture-pass-123"); err != nil {
		t.Fatal(err)
	}
	if err := disabled.db.SaveAccessToken("fixture-admin-delete"); err != nil {
		t.Fatal(err)
	}
	if got := imageCacheSnapshotOf(t, disabled, "fixture-admin-delete"); got.Enabled || got.Dir != "" {
		t.Fatalf("disabled snapshot: %+v", got)
	}
	if w := imageCacheCall(disabled, "PUT", "/image-cache/settings", "fixture-admin-delete", imageCacheETag(0), `{"max_bytes":1024}`); w.Code != 409 || !strings.Contains(w.Body.String(), "IMAGE_CACHE_DISABLED") {
		t.Fatalf("disabled save: %d %s", w.Code, w.Body.String())
	}
	// 坏值不该让服务起不来：回落配置文件默认值并告警。
	corrupt, _, corruptPath := authorityApp(t)
	if err := corrupt.db.SetSetting(settingImageCacheMaxBytes, "oops"); err != nil {
		t.Fatal(err)
	}
	corrupt.Close()
	reopened, err := newApp(config.Config{DBPath: corruptPath}, cache.NewMemory(16))
	if err != nil {
		t.Fatalf("corrupt setting blocked startup: %v", err)
	}
	defer reopened.Close()
	if err := reopened.db.InitializeAdministrator("admin", "fixture-pass-123"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.SaveAccessToken("fixture-admin-delete"); err != nil {
		t.Fatal(err)
	}
	if got := imageCacheSnapshotOf(t, reopened, "fixture-admin-delete"); got.MaxBytes != int64(2)<<30 {
		t.Fatalf("fallback max_bytes = %d", got.MaxBytes)
	}
}

func TestImageCacheSettingsAsset(t *testing.T) {
	f := newAdminDeleteFixture(t)
	r := httptest.NewRequest("GET", "/web/image-cache-settings.js", nil)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "mountImageCacheSettings") || w.Header().Get("ETag") == "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	tag := w.Header().Get("ETag")
	r = httptest.NewRequest("GET", "/web/image-cache-settings.js", nil)
	r.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/admin", nil)
	w = httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `/web/image-cache-settings.js`) {
		t.Fatalf("index.html missing script: %d", w.Code)
	}
}

func boolPtr(v bool) *bool { return &v }
