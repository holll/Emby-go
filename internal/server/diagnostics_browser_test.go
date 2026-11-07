package server

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestAdminBackupBrowser 验证诊断页 N-05 备份面板：展示固定目录与最近结果，
// 触发一次真实备份后回到可用状态并显示该次元数据。仅本地有 Chromium 时运行。
func TestAdminBackupBrowser(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		t.Skip("set CHROME_BIN to an existing Chromium executable for the backup browser regression")
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Fatalf("CHROME_BIN: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the backup browser regression")
	}
	if output, err := exec.Command(node, "-e", `if(typeof WebSocket !== 'function') process.exit(1)`).CombinedOutput(); err != nil {
		t.Skipf("Node with native WebSocket is required: %v %s", err, output)
	}

	_, ts, token := contractEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test-reporter=tap", "diagnostics_browser_test.cjs")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(),
		"DIAG_BROWSER_TARGET="+ts.URL,
		"DIAG_BROWSER_TOKEN="+token,
		"CHROME_BIN="+chrome,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backup browser regression: %v\n%s", err, out)
	}
	t.Log(string(out))
}
