package server

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"runtime/metrics"
	"testing"
	"time"
)

// gcCPUSample 是某个 GC 档位下跑一段分配负载后的 CPU 账。
type gcCPUSample struct {
	percent      int
	gcSeconds    float64 // GC 自身消耗的 CPU 秒（含所有 P）
	totalSeconds float64 // 进程总 CPU 秒（含 idle）
	gcShare      float64 // gcSeconds / totalSeconds
	usefulShare  float64 // 用户态 CPU 占比，用来交叉核对
	cycles       uint64
	nsPerOp      float64
}

func readMetric(t *testing.T, name string) metrics.Sample {
	t.Helper()
	samples := []metrics.Sample{{Name: name}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindBad {
		t.Fatalf("运行时指标 %s 不可用（Go 版本过老？）", name)
	}
	return samples[0]
}

func cpuClassSeconds(t *testing.T, name string) float64 {
	t.Helper()
	return readMetric(t, name).Value.Float64()
}

func gcCycles(t *testing.T) uint64 {
	t.Helper()
	return readMetric(t, "/gc/cycles/total:gc-cycles").Value.Uint64()
}

// gcAllocPayload 造一份接近「管理端宽页 200 条」体积的 JSON 负载（约 400 KB），
// 让分配率落在真实服务端的量级上。
func gcAllocPayload() []byte {
	type item struct {
		ID      int      `json:"id"`
		Name    string   `json:"name"`
		Overvie string   `json:"overview"`
		Genres  []string `json:"genres"`
		People  []string `json:"people"`
		Path    string   `json:"path"`
		Size    int64    `json:"size"`
	}
	items := make([]item, 200)
	for i := range items {
		items[i] = item{
			ID:      i,
			Name:    fmt.Sprintf("示例影片 %03d", i),
			Overvie: "一段用于撑起分配量的简介文本，长度接近真实条目。",
			Genres:  []string{"Action", "Drama", "Sci-Fi"},
			People:  []string{"演员甲", "演员乙", "导演丙"},
			Path:    fmt.Sprintf("/media/library/movie-%03d/movie.strm", i),
			Size:    int64(i) * 1_000_000,
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		panic(err)
	}
	return raw
}

// measureGCShare 在指定 GOGC 下跑一段固定量级的 JSON 编解码 + 监控快照负载，
// 用 runtime/metrics 的 /cpu/classes/gc/total:cpu-seconds 直接读出 GC 真实 CPU。
func measureGCShare(t *testing.T, percent int, iterations int) gcCPUSample {
	t.Helper()
	previous := debug.SetGCPercent(percent)
	defer debug.SetGCPercent(previous)

	payload := gcAllocPayload()
	monitor := newRequestMonitor(time.Now())
	at := time.Now().Add(-2 * time.Minute)

	// 预热一轮，避免把首次 mmap/编译路径算进测量。
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("预热: %v", err)
	}

	gcBefore := cpuClassSeconds(t, "/cpu/classes/gc/total:cpu-seconds")
	totalBefore := cpuClassSeconds(t, "/cpu/classes/total:cpu-seconds")
	userBefore := cpuClassSeconds(t, "/cpu/classes/user:cpu-seconds")
	cyclesBefore := gcCycles(t)

	started := time.Now()
	for i := 0; i < iterations; i++ {
		var decoded any
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("解码: %v", err)
		}
		if _, err := json.Marshal(decoded); err != nil {
			t.Fatalf("编码: %v", err)
		}
		monitor.record(at, 17*time.Millisecond, 200)
		if i%64 == 0 {
			monitor.snapshot(at, 5)
		}
	}
	elapsed := time.Since(started)

	gcSeconds := cpuClassSeconds(t, "/cpu/classes/gc/total:cpu-seconds") - gcBefore
	totalSeconds := cpuClassSeconds(t, "/cpu/classes/total:cpu-seconds") - totalBefore
	userSeconds := cpuClassSeconds(t, "/cpu/classes/user:cpu-seconds") - userBefore
	sample := gcCPUSample{
		percent:      percent,
		gcSeconds:    gcSeconds,
		totalSeconds: totalSeconds,
		cycles:       gcCycles(t) - cyclesBefore,
		nsPerOp:      float64(elapsed.Nanoseconds()) / float64(iterations),
	}
	if totalSeconds > 0 {
		sample.gcShare = gcSeconds / totalSeconds
		sample.usefulShare = userSeconds / totalSeconds
	}
	return sample
}

// BenchmarkGCShareWorkload 让同一段负载可以被单独跑，用来复核 TestGCShare 的数字。
func BenchmarkGCShareWorkload(b *testing.B) {
	payload := gcAllocPayload()
	monitor := newRequestMonitor(time.Now())
	at := time.Now().Add(-2 * time.Minute)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var decoded any
		if err := json.Unmarshal(payload, &decoded); err != nil {
			b.Fatalf("解码: %v", err)
		}
		if _, err := json.Marshal(decoded); err != nil {
			b.Fatalf("编码: %v", err)
		}
		monitor.record(at, 17*time.Millisecond, 200)
		if i%64 == 0 {
			monitor.snapshot(at, 5)
		}
	}
}

// TestGCShareMeasured 量化「GC 到底吃了多少 CPU」，并验证 §2.3 P2-6 的调优方向：
// 抬高 GOGC 后 GC 次数必须下降、GC CPU 占比不得上升。
// 这不是性能门禁（机器负载会漂移），而是把评审文档「GC 占 10–12% CPU」的说法
// 用 runtime/metrics 的实测账落到本仓库的负载上。
func TestGCShareMeasured(t *testing.T) {
	const iterations = 320
	base := measureGCShare(t, 100, iterations)
	tuned := measureGCShare(t, 200, iterations)

	report := func(s gcCPUSample) string {
		return fmt.Sprintf("GOGC=%d: GC 占用 %.1f%% 的进程 CPU（%.3fs/%.3fs）、%d 次 GC、%.2f ms/轮",
			s.percent, s.gcShare*100, s.gcSeconds, s.totalSeconds, s.cycles, s.nsPerOp/1e6)
	}
	t.Log(report(base))
	t.Log(report(tuned))

	if tuned.cycles >= base.cycles {
		t.Errorf("GOGC=200 的 GC 次数（%d）没有低于 GOGC=100（%d）：堆目标翻倍应当减半 GC 次数",
			tuned.cycles, base.cycles)
	}
	// 噪声容差：本机 2 核、其他测试可能同时在跑，允许 15% 的抖动，只要求方向不反。
	if base.gcShare > 0 && tuned.gcShare > base.gcShare*1.15 {
		t.Errorf("GOGC=200 的 GC CPU 占比（%.1f%%）高于 GOGC=100（%.1f%%）超过 15%%，调优方向错了",
			tuned.gcShare*100, base.gcShare*100)
	}
}
