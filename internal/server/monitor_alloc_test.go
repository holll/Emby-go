package server

import (
	"encoding/json"
	"testing"
	"time"
)

// TestMonitorSnapshotAllocs 钉住 snapshot 的稳态分配上限。
// 改造前基线（分片版，series 已预分配、时间串仍逐点 Format）是 68 allocs/op：
// 60 个 RFC3339 时间串 + 被观测点的 5 个指针字段 + counts 的 2 个分位指针 + series 后备数组。
// 分钟文本记忆化后稳态只剩 series 后备数组、被观测点那 5 个指针与 counts 的 2 个分位指针。
// 上限比实测多留 2 个余量以免运行时小改动造成假失败，但足以拦住「回到逐点 Format」的回归。
func TestMonitorSnapshotAllocs(t *testing.T) {
	m := newRequestMonitor(time.Now())
	now := time.Now()
	m.record(now, time.Millisecond, 200)
	m.snapshot(now, 60) // 预热：把本次窗口的 60 个分钟槽填满，测量只反映稳态。
	allocs := testing.AllocsPerRun(50, func() {
		m.snapshot(now, 60)
	})
	if allocs > 10 {
		t.Fatalf("snapshot allocs/op = %v, want <= 10 (逐点 Format 的旧实现是 68)", allocs)
	}
}

// TestMonitorMinuteTextMatchesFormat 逐分钟对比缓存文本与 time.Format 的结果，
// 覆盖 0 分钟、负分钟（1970 之前）以及同一槽位被不同分钟复用后的正确性。
func TestMonitorMinuteTextMatchesFormat(t *testing.T) {
	var cache monitorMinuteText
	base := time.Date(2026, 10, 29, 22, 7, 0, 0, time.UTC).Unix() / 60
	minutes := []int64{
		0, 1, -1, -59, -60, -61, -120,
		base - 60, base - 59, base - 1, base, base + 1, base + 59, base + 60, base + 61,
	}
	for _, minute := range minutes {
		want := time.Unix(minute*60, 0).UTC().Format(time.RFC3339)
		if got := cache.text(minute); got != want {
			t.Fatalf("minute %d: %q != %q", minute, got, want)
		}
	}
	// 同槽复用：minute 与 minute+60 落在同一槽，先后取用都必须是各自的分钟。
	for _, minute := range []int64{base, base + 60, base, base - 60, base + 120} {
		want := time.Unix(minute*60, 0).UTC().Format(time.RFC3339)
		if got := cache.text(minute); got != want {
			t.Fatalf("复用槽位 minute %d: %q != %q", minute, got, want)
		}
	}
}

// marshalMonitorJSON 按 dashboard 的方式序列化快照里对外暴露的两部分（requests、series）。
func marshalMonitorJSON(t *testing.T, s monitorSnapshot) []byte {
	t.Helper()
	data, err := json.Marshal(struct {
		Requests monitorCounts  `json:"requests"`
		Series   []monitorPoint `json:"series"`
	}{s.counts, s.series})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// TestMonitorSnapshotJSONUnchanged 直接对比改造后与 legacy oracle 的 JSON 字节：
// 分钟文本记忆化后，字段名、字段顺序、Start 文本与全部数值都必须逐字节相等。
func TestMonitorSnapshotJSONUnchanged(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 0, 30, 0, time.UTC)
	legacy := newLegacyMonitor(start)
	sharded := newRequestMonitor(start)
	for _, event := range monitorFixtureEvents() {
		at := start.Add(time.Duration(event.minute)*time.Minute + 2*time.Second)
		legacy.record(at, event.elapsed, event.status)
		sharded.record(at, event.elapsed, event.status)
	}
	for _, minutes := range []int{5, 15, 60} {
		now := start.Add(3*time.Minute + 40*time.Second)
		got := string(marshalMonitorJSON(t, sharded.snapshot(now, minutes)))
		want := string(marshalMonitorJSON(t, legacy.snapshot(now, minutes)))
		if got != want {
			t.Fatalf("window %dm: JSON 不一致\n got=%s\nwant=%s", minutes, got, want)
		}
	}
}
