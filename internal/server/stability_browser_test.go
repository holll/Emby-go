package server

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// Unlike the static layout fixture this exercises embedded assets and real HTTP
// mutations. Everything belongs to t.TempDir and an ephemeral loopback listener.
func TestStabilityLiveMobileManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("live browser integration")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not installed; no automatic installation")
	}
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		chrome = "/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("Chromium is not installed; set CHROME_BIN to enable")
	}
	root := t.TempDir()
	library := filepath.Join(root, "media")
	if err := os.Mkdir(library, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "mobile.strm"), []byte("https://media.invalid/mobile.mp4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "mobile.nfo"), []byte(`<movie><title>Mobile stability sample</title><originaltitle>Original mobile sample</originaltitle></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(128))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "stability_browser.cjs")
	// Let the runner terminate its own Chrome child before the hard deadline.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(), "EMBY_STABILITY_URL="+srv.URL, "EMBY_STABILITY_LIBRARY="+library, "CHROME_BIN="+chrome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("live mobile browser: %v\n%s", err, out)
	}
	t.Log(string(out))
}
