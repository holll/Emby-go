package logging

import (
	"strings"
	"testing"
	"time"
)

// TestRingKeepsNewestAndBounds 验证环形缓冲只保留最近条目且按时间正序输出。
func TestRingKeepsNewestAndBounds(t *testing.T) {
	ring := &Ring{entries: make([]Entry, 3)}
	for i := 0; i < 5; i++ {
		ring.add(Entry{Message: string(rune('a' + i))})
	}
	got := ring.Snapshot(0)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Message != "c" || got[2].Message != "e" {
		t.Fatalf("顺序错误: %+v", got)
	}
	if one := ring.Snapshot(1); len(one) != 1 || one[0].Message != "e" {
		t.Fatalf("limit=1: %+v", one)
	}
}

// TestRingWriterParsesLinesAndRedacts 验证按行解析 level、时间，且敏感值脱敏。
func TestRingWriterParsesLinesAndRedacts(t *testing.T) {
	ring := &Ring{entries: make([]Entry, 10)}
	w := &RingWriter{ring: ring, now: func() time.Time { return time.Unix(0, 0) }}
	if _, err := w.Write([]byte("time=2026-10-19T10:00:00Z level=ERROR msg=\"login failed\" token=abcdef123456\n")); err != nil {
		t.Fatal(err)
	}
	entries := ring.Snapshot(0)
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
	entry := entries[0]
	if entry.Level != "ERROR" || entry.Time != "2026-10-19T10:00:00Z" {
		t.Fatalf("解析失败: %+v", entry)
	}
	if strings.Contains(entry.Message, "abcdef123456") || !strings.Contains(entry.Message, "[REDACTED]") {
		t.Fatalf("未脱敏: %s", entry.Message)
	}
}
