package server

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"runtime/metrics"
	"testing"
	"time"
)

func readMetric(t testing.TB, name string) metrics.Sample {
	t.Helper()
	samples := []metrics.Sample{{Name: name}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindBad {
		t.Fatalf("运行时指标 %s 不可用（Go 版本过老？）", name)
	}
	return samples[0]
}

func cpuClassSeconds(t testing.TB, name string) float64 {
	t.Helper()
	return readMetric(t, name).Value.Float64()
}

func gcCycles(t testing.TB) uint64 {
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

// BenchmarkGCShareWorkload compares the same workload at both GOGC settings.
// CPU share and GC cycles are observations, not functional assertions.
func BenchmarkGCShareWorkload(b *testing.B) {
	payload := gcAllocPayload()
	for _, percent := range []int{100, 200} {
		b.Run(fmt.Sprintf("GOGC=%d", percent), func(b *testing.B) {
			previous := debug.SetGCPercent(percent)
			defer debug.SetGCPercent(previous)
			monitor := newRequestMonitor(time.Now())
			at := time.Now().Add(-2 * time.Minute)
			const gcCPU = "/cpu/classes/gc/total:cpu-seconds"
			const totalCPU = "/cpu/classes/total:cpu-seconds"
			b.ReportAllocs()
			gcBefore := cpuClassSeconds(b, gcCPU)
			totalBefore := cpuClassSeconds(b, totalCPU)
			cyclesBefore := gcCycles(b)
			for i := 0; b.Loop(); i++ {
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
			if total := cpuClassSeconds(b, totalCPU) - totalBefore; total > 0 {
				b.ReportMetric(100*(cpuClassSeconds(b, gcCPU)-gcBefore)/total, "gc-cpu-%")
			}
			b.ReportMetric(float64(gcCycles(b)-cyclesBefore)/float64(b.N), "gc-cycles/op")
		})
	}
}
