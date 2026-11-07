package server

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"emby-go/internal/upgrade"
)

// TestAdminUpdateBrowser 验证真实浏览器中的 U-01 入口：页面打开自动检查、
// 左下角徽标、更新面板，以及下载失败时面板内可读的失败原因。
// 只有本地存在 Chromium 时才跑；Go 夹具用假 Release，Node 不访问 GitHub。
func TestAdminUpdateBrowser(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		t.Skip("set CHROME_BIN to an existing Chromium executable for the upgrade browser regression")
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Fatalf("CHROME_BIN: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the upgrade browser regression")
	}
	if output, err := exec.Command(node, "-e", `if(typeof WebSocket !== 'function') process.exit(1)`).CombinedOutput(); err != nil {
		t.Skipf("Node with native WebSocket is required: %v %s", err, output)
	}

	const tag = "v9.9.9-abc1234"
	app, ts, token := contractEnv(t)
	// 二进制下载返回 500：apply 必须走到 failed，旧程序不能被替换。
	srv, _ := updateTestAPI(t, tag, http.StatusInternalServerError)
	app.updater = upgrade.New(upgrade.Options{
		Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test-reporter=tap", "update_browser_test.cjs")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	viewer := viewerToken(t, ts, token)
	cmd.Env = append(os.Environ(),
		"UPDATE_BROWSER_TARGET="+ts.URL,
		"UPDATE_BROWSER_TOKEN="+token,
		"UPDATE_BROWSER_VIEWER_TOKEN="+viewer,
		"UPDATE_BROWSER_TAG="+tag,
		"CHROME_BIN="+chrome,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upgrade browser regression: %v\n%s", err, out)
	}
	t.Log(string(out))
}
