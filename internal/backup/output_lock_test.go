package backup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeLock 在输出目录里造一个锁文件，用于模拟「上一个持锁者留下的锁」。
func writeLock(t *testing.T, outDir, content string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outDir, lockFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// deadPID 返回一个「刚刚退出」的进程 pid，用于模拟被 kill -9 的持锁者。
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Skipf("无法构造已退出进程: %v", err)
	}
	return cmd.Process.Pid
}

// TestRunTakesOverLockOfDeadProcess 验证持锁进程已被 kill -9 时，下一次备份能接管，
// 不需要人工删除锁文件（容器被 kill、机器重启都会留下这种锁）。
func TestRunTakesOverLockOfDeadProcess(t *testing.T) {
	dbPath, _, outDir := fixture(t)
	writeLock(t, outDir, "pid="+strconv.Itoa(deadPID(t))+"\nstart=2026-10-18T00:00:00Z\n", 0)
	result, err := Run(Options{DBPath: dbPath, OutDir: outDir, Now: time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("持锁进程已死亡，接管应成功: %v", err)
	}
	if err := checkSnapshot(result.Database); err != nil {
		t.Fatalf("接管后的备份不完整: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, lockFileName)); !os.IsNotExist(err) {
		t.Errorf("备份结束后锁文件仍在: %v", err)
	}
}

// TestRunRejectsLockHeldByLiveProcess 验证持锁进程仍活着时明确失败，
// 且绝不清理别人的锁、不写任何产物（并发写同一输出目录就是 P2-2 的数据损坏路径）。
func TestRunRejectsLockHeldByLiveProcess(t *testing.T) {
	dbPath, _, outDir := fixture(t)
	path := writeLock(t, outDir, "pid="+strconv.Itoa(os.Getpid())+"\nstart=2026-10-18T00:00:00Z\n", 0)
	if _, err := Run(Options{DBPath: dbPath, OutDir: outDir, Now: time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)}); !errors.Is(err, ErrOutputBusy) {
		t.Fatalf("err = %v，期望 ErrOutputBusy", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("不应删除别人持有的锁: %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("被拒绝的备份不应留下产物: %v", entries)
	}
}

// TestRunLockUnknownOwnerUsesAgeBackoff 验证锁文件写坏（拿不到 pid）时：
// 新锁按「无法确认持锁者」拒绝，超时的陈旧锁才接管。
func TestRunLockUnknownOwnerUsesAgeBackoff(t *testing.T) {
	now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		age      time.Duration
		wantBusy bool
	}{
		{name: "fresh-unknown", age: 0, wantBusy: true},
		{name: "stale-unknown", age: lockStaleAfter + time.Minute, wantBusy: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, _, outDir := fixture(t)
			// 空文件模拟「创建了锁但还没来得及写 pid 就崩溃」。
			writeLock(t, outDir, "", tc.age)
			_, err := Run(Options{DBPath: dbPath, OutDir: outDir, Now: now})
			if tc.wantBusy {
				if !errors.Is(err, ErrOutputBusy) {
					t.Fatalf("err = %v，期望 ErrOutputBusy", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("陈旧锁应被接管: %v", err)
			}
		})
	}
}

// TestRunReleasesLockOnFailure 验证失败路径同样释放锁：否则一次坏源库备份
// 会把输出目录锁死到 lockStaleAfter。
func TestRunReleasesLockOnFailure(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "bad.db")
	if err := os.WriteFile(dbPath, []byte("definitely not sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "backups")
	if _, err := Run(Options{DBPath: dbPath, OutDir: outDir, Now: time.Unix(0, 0)}); err == nil {
		t.Fatal("损坏的源库不应备份成功")
	}
	if _, err := os.Stat(filepath.Join(outDir, lockFileName)); !os.IsNotExist(err) {
		t.Fatalf("失败路径没有释放锁文件: %v", err)
	}
}
