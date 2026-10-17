package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func newShutdownApp(t *testing.T) *App {
	t.Helper()
	app, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "shutdown.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	t.Cleanup(app.Close)
	return app
}

func getHealth(t *testing.T, app *App, path string) (int, map[string]string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	app.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s body: %v (%q)", path, err, w.Body.String())
	}
	return w.Code, body, w.Header().Get("Cache-Control")
}

func TestHealthReflectsReadiness(t *testing.T) {
	app := newShutdownApp(t)
	for _, path := range []string{"/api/health", "/healthz"} {
		code, body, cc := getHealth(t, app, path)
		if code != http.StatusOK || body["status"] != "ok" || cc != "no-store" {
			t.Fatalf("%s before shutdown: code=%d body=%v cache=%q", path, code, body, cc)
		}
	}
	app.BeginShutdown()
	for _, path := range []string{"/api/health", "/healthz"} {
		code, body, cc := getHealth(t, app, path)
		if code != http.StatusServiceUnavailable || body["status"] != "shutting_down" || cc != "no-store" {
			t.Fatalf("%s after shutdown: code=%d body=%v cache=%q", path, code, body, cc)
		}
	}
}

func TestHealthStaysPublicBeforeInitialization(t *testing.T) {
	app := newShutdownApp(t)
	code, body, _ := getHealth(t, app, "/api/health")
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("health must not require auth: code=%d body=%v", code, body)
	}
}

func TestBeginShutdownCancelsWorkersAndIsIdempotent(t *testing.T) {
	app := newShutdownApp(t)
	probeCtx, probeCancel := context.WithCancel(context.Background())
	scrapeCtx, scrapeCancel := context.WithCancel(context.Background())
	app.probeTaskMu.Lock()
	app.probeCtx, app.probeCancel = probeCtx, probeCancel
	app.probeTaskMu.Unlock()
	app.scrapeTaskMu.Lock()
	app.scrapeCtx, app.scrapeCancel = scrapeCtx, scrapeCancel
	app.scrapeTaskMu.Unlock()

	app.BeginShutdown()
	app.BeginShutdown()

	if probeCtx.Err() == nil {
		t.Fatal("BeginShutdown 未取消探测 worker")
	}
	if scrapeCtx.Err() == nil {
		t.Fatal("BeginShutdown 未取消刮削 worker")
	}
	if app.ready.Load() {
		t.Fatal("BeginShutdown 后 readiness 仍为 true")
	}
}

func TestCloseIsIdempotentAndFlipsReadiness(t *testing.T) {
	app := newShutdownApp(t)
	app.Close()
	app.Close()
	if code, _, _ := getHealth(t, app, "/api/health"); code != http.StatusServiceUnavailable {
		t.Fatalf("Close 后 health 应 503，得到 %d", code)
	}
}

func TestWaitBackgroundHonorsDeadline(t *testing.T) {
	app := newShutdownApp(t)
	app.workerWG.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := app.WaitBackground(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("预算耗尽应返回 DeadlineExceeded，得到 %v", err)
	}

	app.workerWG.Done()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := app.WaitBackground(ctx2); err != nil {
		t.Fatalf("worker 结束后应无错误，得到 %v", err)
	}
}

func TestWaitBackgroundWaitsWorkers(t *testing.T) {
	app := newShutdownApp(t)
	app.workerWG.Add(1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		app.workerWG.Done()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := app.WaitBackground(ctx); err != nil {
		t.Fatalf("WaitBackground: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("WaitBackground 未等待 worker：%s", elapsed)
	}
}
