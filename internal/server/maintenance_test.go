package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func TestMaintenanceRunsAnalyzeAndStops(t *testing.T) {
	app, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "maint.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer app.Close()

	called := make(chan struct{}, 1)
	app.analyzeStats = func() error {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	}
	app.startMaintenance()
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("维护任务未执行 ANALYZE")
	}

	app.BeginShutdown()
	done := make(chan struct{})
	go func() {
		_ = app.WaitBackground(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("维护协程未随关闭退出")
	}
}
