package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildVersionAPI(t *testing.T) {
	oldVersion, oldCommit, oldTime := BuildVersion, BuildCommit, BuildTime
	t.Cleanup(func() { BuildVersion, BuildCommit, BuildTime = oldVersion, oldCommit, oldTime })
	BuildVersion = "v1.4.4-ad21a94"
	BuildCommit = "ad21a94123456789012345678901234567890123"
	BuildTime = "2026-10-22T05:00:00Z"
	app := newShutdownApp(t)

	// 版本无需凭据或初始化；代理和客户端不得缓存旧二进制信息。
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
		r.Header.Set("If-None-Match", `"old-build"`)
		app.Handler().ServeHTTP(w, r)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || len(body) != 4 ||
			body["product"] != "Emby-go" || body["version"] != BuildVersion || body["commit"] != BuildCommit || body["build_time"] != BuildTime {
			t.Fatalf("status=%d headers=%v body=%v", w.Code, w.Header(), body)
		}
	}
	// 不污染 Emby 的能力协商版本（它本来就不是程序发布版本）。
	for _, path := range []string{"/System/Info/Public", "/emby/System/Info/Public"} {
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body["Version"] != embyVersion {
			t.Fatalf("%s status=%d body=%v err=%v", path, w.Code, body, err)
		}
	}
}

func TestBuildVersionAPIDevelopmentBuild(t *testing.T) {
	// 无构建注入时诚实返回 dev，而不是把 Emby 兼容版本或主机 git 目录当构建版本。
	if BuildVersion != "dev" || BuildCommit != "none" || BuildTime != "unknown" {
		t.Fatalf("unexpected unlinked defaults: %q %q %q", BuildVersion, BuildCommit, BuildTime)
	}
	app := newShutdownApp(t)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body["version"] != "dev" || body["commit"] != "none" || body["build_time"] != "unknown" {
		t.Fatalf("status=%d body=%v err=%v", w.Code, body, err)
	}
}
