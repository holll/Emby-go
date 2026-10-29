package main

import (
	"runtime/debug"
	"testing"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestRuntimeTuningFromEnv(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		gcPercent int
		memLimit  int64
		wantWarns int
	}{
		{name: "默认值", env: nil, gcPercent: defaultGOGC, memLimit: 0},
		{name: "显式百分比", env: map[string]string{"EMBY_GOGC": "150"}, gcPercent: 150},
		{name: "关闭 GC", env: map[string]string{"EMBY_GOGC": "off"}, gcPercent: -1},
		{name: "关闭 GC 的负数写法", env: map[string]string{"EMBY_GOGC": "-1"}, gcPercent: -1},
		{name: "GOGC=0 等同关闭", env: map[string]string{"EMBY_GOGC": "0"}, gcPercent: -1},
		{name: "非法百分比回落到默认", env: map[string]string{"EMBY_GOGC": "abc"}, gcPercent: defaultGOGC, wantWarns: 1},
		{name: "负百分比之外的空格", env: map[string]string{"EMBY_GOGC": "  200  "}, gcPercent: 200},
		{name: "内存上限字节数", env: map[string]string{"EMBY_GOMEMLIMIT": "1073741824"}, gcPercent: defaultGOGC, memLimit: 1 << 30},
		{name: "内存上限 MiB", env: map[string]string{"EMBY_GOMEMLIMIT": "512MiB"}, gcPercent: defaultGOGC, memLimit: 512 << 20},
		{name: "内存上限 GiB 小写", env: map[string]string{"EMBY_GOMEMLIMIT": "2gib"}, gcPercent: defaultGOGC, memLimit: 2 << 30},
		{name: "内存上限 GB 十进制", env: map[string]string{"EMBY_GOMEMLIMIT": "1GB"}, gcPercent: defaultGOGC, memLimit: 1000 * 1000 * 1000},
		{name: "不设上限", env: map[string]string{"EMBY_GOMEMLIMIT": "max"}, gcPercent: defaultGOGC, memLimit: 0},
		{name: "非法上限不设", env: map[string]string{"EMBY_GOMEMLIMIT": "lots"}, gcPercent: defaultGOGC, memLimit: 0, wantWarns: 1},
		{name: "零上限不设", env: map[string]string{"EMBY_GOMEMLIMIT": "0"}, gcPercent: defaultGOGC, memLimit: 0, wantWarns: 1},
		{name: "两个都设", env: map[string]string{"EMBY_GOGC": "300", "EMBY_GOMEMLIMIT": "768MiB"}, gcPercent: 300, memLimit: 768 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tuning, warns := runtimeTuningFromEnv(envFrom(tc.env))
			if tuning.gcPercent != tc.gcPercent {
				t.Errorf("gcPercent = %d，应为 %d", tuning.gcPercent, tc.gcPercent)
			}
			if tuning.memLimit != tc.memLimit {
				t.Errorf("memLimit = %d，应为 %d", tuning.memLimit, tc.memLimit)
			}
			if len(warns) != tc.wantWarns {
				t.Errorf("警告数 = %d（%v），应为 %d", len(warns), warns, tc.wantWarns)
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{
		"1048576": 1 << 20,
		"1KiB":    1 << 10,
		"8MiB":    8 << 20,
		"1GiB":    1 << 30,
		"2k":      2 << 10,
		"3m":      3 << 20,
		"1g":      1 << 30,
		"1KB":     1000,
		"1MB":     1000 * 1000,
	}
	for raw, want := range cases {
		got, err := parseByteSize(raw)
		if err != nil {
			t.Errorf("parseByteSize(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("parseByteSize(%q) = %d，应为 %d", raw, got, want)
		}
	}
	for _, raw := range []string{"", "abc", "12XB", "-5MiB", "0"} {
		if _, err := parseByteSize(raw); err == nil {
			t.Errorf("parseByteSize(%q) 应报错", raw)
		}
	}
}

// TestApplyRuntimeTuning 用 debug 包自身的「返回旧值」语义验证参数真的落到了运行时。
func TestApplyRuntimeTuning(t *testing.T) {
	previousPercent := debug.SetGCPercent(100)
	previousLimit := debug.SetMemoryLimit(-1)
	t.Cleanup(func() {
		debug.SetGCPercent(previousPercent)
		debug.SetMemoryLimit(previousLimit)
	})

	applyRuntimeTuning(runtimeTuning{gcPercent: 250, memLimit: 3 << 30})
	if got := debug.SetGCPercent(100); got != 250 {
		t.Errorf("SetGCPercent 之前的值 = %d，说明调优没生效（应为 250）", got)
	}
	if got := debug.SetMemoryLimit(-1); got != 3<<30 {
		t.Errorf("SetMemoryLimit 之前的值 = %d，说明上限没生效（应为 %d）", got, int64(3<<30))
	}
}

// TestApplyRuntimeTuningLeavesMemoryLimitAlone memLimit=0 表示「不设」，不能把
// 运维在别处设过的软上限清掉。
func TestApplyRuntimeTuningLeavesMemoryLimitAlone(t *testing.T) {
	previousPercent := debug.SetGCPercent(100)
	previousLimit := debug.SetMemoryLimit(2 << 30)
	t.Cleanup(func() {
		debug.SetGCPercent(previousPercent)
		debug.SetMemoryLimit(previousLimit)
	})

	applyRuntimeTuning(runtimeTuning{gcPercent: 200, memLimit: 0})
	if got := debug.SetMemoryLimit(-1); got != 2<<30 {
		t.Errorf("memLimit=0 时不应改动内存上限，实际变成 %d", got)
	}
}
