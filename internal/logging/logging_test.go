package logging

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDailyWriterRotatesByDay 验证双写与跨天切换文件。
func TestDailyWriterRotatesByDay(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	w := NewDailyWriter(dir, "app", &console)

	day := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return day }
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	day = day.Add(24 * time.Hour)
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := readFile(t, filepath.Join(dir, "app-2026-09-11.log")); got != "first\n" {
		t.Errorf("第一天的文件内容 = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "app-2026-09-12.log")); got != "second\n" {
		t.Errorf("第二天的文件内容 = %q", got)
	}
	if console.String() != "first\nsecond\n" {
		t.Errorf("控制台内容 = %q", console.String())
	}
}

// TestDailyWriterDegradesWhenUnwritable 验证目录不可写时降级为仅控制台且不报错。
func TestDailyWriterDegradesWhenUnwritable(t *testing.T) {
	// 用一个普通文件占住日志目录路径，使 MkdirAll 失败。
	blocked := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	w := NewDailyWriter(blocked, "app", &console)

	n, err := w.Write([]byte("hello\n"))
	if err != nil || n != len("hello\n") {
		t.Fatalf("Write = (%d, %v)，期望不报错", n, err)
	}
	// 同一天内反复写只告警一次，避免刷屏。
	w.Write([]byte("again\n"))
	got := console.String()
	if !bytes.HasPrefix([]byte(got), []byte("hello\n")) {
		t.Errorf("控制台内容 = %q，期望先输出正文", got)
	}
	if n := bytes.Count([]byte(got), []byte("[warn]")); n != 1 {
		t.Errorf("控制台告警次数 = %d，期望 1 次: %q", n, got)
	}
	if err := w.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestDailyWriterAppendsToSameDay 验证同一天多次打开（如重启）不覆盖旧内容。
func TestDailyWriterAppendsToSameDay(t *testing.T) {
	dir := t.TempDir()
	w := NewDailyWriter(dir, "request", &bytes.Buffer{})
	w.now = func() time.Time { return time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC) }
	w.Write([]byte("a\n"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2 := NewDailyWriter(dir, "request", &bytes.Buffer{})
	w2.now = w.now
	w2.Write([]byte("b\n"))
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "request-2026-09-11.log")); got != "a\nb\n" {
		t.Errorf("文件内容 = %q，期望追加而非覆盖", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return string(data)
}

// TestSetupClosesPreviousWriters 验证重复 Setup 关掉上一代 writer 的文件句柄。
//
// Setup 每调一次就新建两个 DailyWriter；不关旧的，每个旧 writer 都留着已打开的
// app/request 日志 fd（测试与嵌入式热重载会反复调，泄漏会累积），而且旧句柄
// 仍指向同一路径，形成两个互不知情的写者。
func TestSetupClosesPreviousWriters(t *testing.T) {
	t.Chdir(t.TempDir())
	// 恢复成不落盘的处理器，避免把全局 slog 留在临时目录里给后面的测试用。
	t.Cleanup(func() { SetupTo(io.Discard, false) })

	first := Setup(false)
	for _, w := range []*DailyWriter{first.app, first.request} {
		if _, err := w.Write([]byte("hello\n")); err != nil {
			t.Fatalf("首代 writer 写入失败: %v", err)
		}
	}
	first.app.mu.Lock()
	appFile, requestFile := first.app.file, first.request.file
	first.app.mu.Unlock()
	if appFile == nil || requestFile == nil {
		t.Fatal("首代 writer 没有打开日志文件，测试前提不成立")
	}

	second := Setup(false)
	if second == first {
		t.Fatal("Setup 应返回新一代 Loggers")
	}
	// 关闭后的 *os.File 再写必然失败；仍能写说明旧句柄没被关掉。
	if _, err := appFile.Write([]byte("again\n")); err == nil {
		t.Error("重复 Setup 未关闭旧 app writer 的文件句柄")
	}
	if _, err := requestFile.Write([]byte("again\n")); err == nil {
		t.Error("重复 Setup 未关闭旧 request writer 的文件句柄")
	}
	// 新一代必须可用（不能因为关旧 writer 而把新 writer 也关掉）。
	if _, err := second.app.Write([]byte("new\n")); err != nil {
		t.Errorf("新一代 writer 写入失败: %v", err)
	}
}
