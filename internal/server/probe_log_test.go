package server

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"emby-go/internal/store"
)

// TestProbeProgressLogShape 覆盖 R6 的日志形状：文件名编号、去重、控制字符清理、
// CD 标记、结构化字段与失败安全分类；empty number 不得用数据库 ID 冒充。
func TestProbeProgressLogShape(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	previous := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{mu: &mu, lines: &lines}))
	defer slog.SetDefault(previous)

	a := &App{}
	a.emitProbeProgress(9, probeTarget{Movie: store.Movie{ID: 101, Title: "标题\r\n注入\x00"}, Primary: true, Index: 1}, "success", "PROBED", 1, 2)
	a.emitProbeProgress(9, probeTarget{Movie: store.Movie{ID: 102, Number: "ABC-123", Title: "ABC-123 标题"}, Primary: true, Index: 1}, "skipped", "CACHE_CURRENT", 2, 2)
	a.emitProbeProgress(9, probeTarget{Movie: store.Movie{ID: 103, Number: "XYZ-9", Title: "标题"}, Primary: false, Index: 3}, "failed", "FFPROBE_FAILED", 3, 3)

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	if strings.ContainsAny(lines[0], "\r\n\x00") {
		t.Fatalf("progress line contains control characters: %q", lines[0])
	}
	for _, want := range []string{
		"媒体信息探测[1/2]，当前标题 注入",
		"task_id=9", "item_id=101", "done=1", "total=2", "part=1", "status=success",
		"媒体信息探测[2/2]，当前ABC-123 标题",
		"ABC-123 ABC-123",
		"（CD3）", "status=failed", "safe_error=FFPROBE_FAILED",
	} {
		if want == "ABC-123 ABC-123" {
			if strings.Contains(joined, want) {
				t.Fatalf("number duplicated in title: %s", joined)
			}
			continue
		}
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

// TestCompleteProbeCountsOnceAndOrdersProgress 断言每次 completeProbe 只计一次，
// 且进度行严格按 done 递增输出（磁盘顺序由 progressEmitMu 保证）。
func TestCompleteProbeCountsOnceAndOrdersProgress(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	previous := slog.Default()
	slog.SetDefault(slog.New(&captureHandler{mu: &mu, lines: &lines}))
	defer slog.SetDefault(previous)

	a := &App{}
	a.probeStatus.Total = 2
	target := probeTarget{Movie: store.Movie{ID: 1, Title: "A"}, Primary: true, Index: 1}
	a.completeProbe(5, target, probeOutcome{Status: "success", SafeErrorCode: "PROBED"})
	a.completeProbe(5, target, probeOutcome{Status: "skipped", SafeErrorCode: "CACHE_CURRENT"})
	a.completeProbe(5, target, probeOutcome{Status: "cancelled"})

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	if a.probeStatus.Done != 2 || a.probeStatus.Success != 1 || a.probeStatus.Skipped != 1 {
		t.Fatalf("bad counters: %+v", a.probeStatus)
	}
	first := strings.Index(joined, "媒体信息探测[1/2]")
	second := strings.Index(joined, "媒体信息探测[2/2]")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("progress order broken:\n%s", joined)
	}
	if strings.Contains(joined, "[3/2]") {
		t.Fatalf("cancelled target counted: %s", joined)
	}
}
