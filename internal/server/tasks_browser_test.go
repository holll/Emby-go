package server

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestAdminTasksBrowser 验证单独任务中心页：导航、表格列、筛选与刷新按钮可用。
func TestAdminTasksBrowser(t *testing.T) {
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		t.Skip("set CHROME_BIN to an existing Chromium executable for the tasks browser regression")
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Fatalf("CHROME_BIN: %v", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the tasks browser regression")
	}
	if output, err := exec.Command(node, "-e", `if(typeof WebSocket !== 'function') process.exit(1)`).CombinedOutput(); err != nil {
		t.Skipf("Node with native WebSocket is required: %v %s", err, output)
	}

	_, ts, token := contractEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test-reporter=tap", "tasks_browser_test.cjs")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(),
		"TASKS_BROWSER_TARGET="+ts.URL,
		"TASKS_BROWSER_TOKEN="+token,
		"CHROME_BIN="+chrome,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tasks browser regression: %v\n%s", err, out)
	}
	t.Log(string(out))
}
