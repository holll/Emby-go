package logging

import (
	"bytes"
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
