package server

import (
	"math"
	"sync"
	"testing"
	"time"
)

// legacyMonitor 是分片改造前的实现（单锁 + 单份 [60] 环），仅用于测试里做
// 「统计口径逐项相等」的对照 oracle。逻辑必须与改造前的 monitor.go 保持一致。
type legacyMonitor struct {
	mu         sync.Mutex
	started    time.Time
	instanceID string
	buckets    [monitorRetention]monitorBucket
}

func newLegacyMonitor(started time.Time) *legacyMonitor {
	return &legacyMonitor{started: started.UTC(), instanceID: "legacy"}
}

func (m *legacyMonitor) record(at time.Time, elapsed time.Duration, status int) {
	minute := at.Unix() / 60
	index := minute % monitorRetention
	if index < 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := &m.buckets[index]
	if b.initialized && b.minute > minute {
		return
	}
	if !b.initialized || b.minute != minute {
		*b = monitorBucket{minute: minute, initialized: true}
	}
	b.total++
	if status >= 400 && status < 500 {
		b.errors4xx++
	}
	if status >= 500 && status < 600 {
		b.errors5xx++
	}
	bucket := len(monitorBoundsMS)
	for i, upper := range monitorBoundsMS {
		if elapsed <= time.Duration(upper)*time.Millisecond {
			bucket = i
			break
		}
	}
	b.histogram[bucket]++
}

func (m *legacyMonitor) snapshot(now time.Time, minutes int) monitorSnapshot {
	current := now.Unix() / 60
	first := current - int64(minutes) + 1
	out := monitorSnapshot{start: time.Unix(first*60, 0).UTC(), end: now.UTC(), series: make([]monitorPoint, 0, minutes)}
	observedStart := out.start
	if m.started.After(observedStart) {
		observedStart = m.started
	}
	out.counts.SampledSeconds = math.Max(0, now.Sub(observedStart).Seconds())
	var hist [len(monitorBoundsMS) + 1]uint64
	m.mu.Lock()
	defer m.mu.Unlock()
	for minute := first; minute <= current; minute++ {
		point := monitorPoint{Start: time.Unix(minute*60, 0).UTC().Format(time.RFC3339), Observed: minute >= m.started.Unix()/60 && !now.Before(m.started)}
		if point.Observed {
			var b monitorBucket
			index := minute % monitorRetention
			if index >= 0 && m.buckets[index].initialized && m.buckets[index].minute == minute {
				b = m.buckets[index]
			}
			total, e4, e5 := b.total, b.errors4xx, b.errors5xx
			point.Requests, point.Errors4xx, point.Errors5xx = &total, &e4, &e5
			point.P50, point.P50Overflow = monitorQuantile(b.histogram, b.total, .50)
			point.P95, point.P95Overflow = monitorQuantile(b.histogram, b.total, .95)
			out.counts.Total += b.total
			out.counts.Errors4xx += b.errors4xx
			out.counts.Errors5xx += b.errors5xx
			for i, n := range b.histogram {
				hist[i] += n
			}
		}
		out.series = append(out.series, point)
	}
	out.counts.P50, out.counts.P50Overflow = monitorQuantile(hist, out.counts.Total, .50)
	out.counts.P95, out.counts.P95Overflow = monitorQuantile(hist, out.counts.Total, .95)
	if out.counts.SampledSeconds > 0 {
		rate := float64(out.counts.Total) / out.counts.SampledSeconds
		out.counts.Rate = &rate
	}
	return out
}

func sameFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// compareSnapshots 逐项比较两个快照：总量、错误数、分位、溢出标记、速率与
// 每一分钟的 series（Requests/Errors/P50/P95/Observed/Start）。
func compareSnapshots(t *testing.T, label string, got, want monitorSnapshot) {
	t.Helper()
	if !got.start.Equal(want.start) || !got.end.Equal(want.end) {
		t.Fatalf("%s: 时间窗 [%v,%v] != [%v,%v]", label, got.start, got.end, want.start, want.end)
	}
	gc, wc := got.counts, want.counts
	if gc.Total != wc.Total || gc.Errors4xx != wc.Errors4xx || gc.Errors5xx != wc.Errors5xx ||
		gc.P50Overflow != wc.P50Overflow || gc.P95Overflow != wc.P95Overflow ||
		gc.SampledSeconds != wc.SampledSeconds || !sameFloatPtr(gc.P50, wc.P50) || !sameFloatPtr(gc.P95, wc.P95) || !sameFloatPtr(gc.Rate, wc.Rate) {
		t.Fatalf("%s: counts 不一致\n got=%+v\nwant=%+v", label, gc, wc)
	}
	if len(got.series) != len(want.series) {
		t.Fatalf("%s: series 长度 %d != %d", label, len(got.series), len(want.series))
	}
	for i := range got.series {
		g, w := got.series[i], want.series[i]
		if g.Start != w.Start || g.Observed != w.Observed || !sameFloatPtr(g.P50, w.P50) || !sameFloatPtr(g.P95, w.P95) ||
			g.P50Overflow != w.P50Overflow || g.P95Overflow != w.P95Overflow ||
			(g.Requests == nil) != (w.Requests == nil) || (g.Errors4xx == nil) != (w.Errors4xx == nil) || (g.Errors5xx == nil) != (w.Errors5xx == nil) {
			t.Fatalf("%s: series[%d] 不一致\n got=%+v\nwant=%+v", label, i, g, w)
		}
		if g.Requests != nil && (*g.Requests != *w.Requests || *g.Errors4xx != *w.Errors4xx || *g.Errors5xx != *w.Errors5xx) {
			t.Fatalf("%s: series[%d] 计数不一致 got=%d/%d/%d want=%d/%d/%d", label, i, *g.Requests, *g.Errors4xx, *g.Errors5xx, *w.Requests, *w.Errors4xx, *w.Errors5xx)
		}
	}
}

// monitorFixtureEvents 是一组固定的 (分钟偏移, 延迟, 状态码) 序列：覆盖直方图
// 每个边界（含恰好等于边界与刚超边界）、4xx/5xx、溢出、以及跨分钟与小时级
// 回绕（同一环形槽被更旧的一分钟撞上）。固定后新旧实现必须逐项相等。
func monitorFixtureEvents() []struct {
	minute  int64
	elapsed time.Duration
	status  int
} {
	latencies := []time.Duration{
		time.Microsecond, time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
		20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
		30 * time.Second, time.Minute, time.Minute + time.Nanosecond, 2 * time.Minute,
	}
	statuses := []int{200, 200, 200, 204, 301, 400, 404, 499, 500, 503, 599, 200}
	var events []struct {
		minute  int64
		elapsed time.Duration
		status  int
	}
	for minute := int64(0); minute < 3; minute++ {
		for i, latency := range latencies {
			events = append(events, struct {
				minute  int64
				elapsed time.Duration
				status  int
			}{minute, latency, statuses[(int(minute)+i)%len(statuses)]})
		}
	}
	// 环形槽回绕：更旧的一分钟撞上已被更近一分钟占用的槽，必须被忽略。
	events = append(events,
		struct {
			minute  int64
			elapsed time.Duration
			status  int
		}{63, 3 * time.Millisecond, 200},
		struct {
			minute  int64
			elapsed time.Duration
			status  int
		}{3, 4 * time.Millisecond, 500},
	)
	return events
}

// TestMonitorShardMatchesLegacy 是改造的语义闸门：固定事件序列下，分片实现与
// 改造前单锁实现在同一时刻的快照必须逐项相等（总量/错误数/分位/速率/series）。
func TestMonitorShardMatchesLegacy(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 0, 30, 0, time.UTC)
	legacy := newLegacyMonitor(start)
	sharded := newRequestMonitor(start)
	base := start
	for _, event := range monitorFixtureEvents() {
		at := base.Add(time.Duration(event.minute)*time.Minute + 2*time.Second)
		legacy.record(at, event.elapsed, event.status)
		sharded.record(at, event.elapsed, event.status)
	}
	for _, minutes := range []int{5, 15, 60} {
		now := base.Add(3*time.Minute + 40*time.Second)
		compareSnapshots(t, "窗口内", sharded.snapshot(now, minutes), legacy.snapshot(now, minutes))
	}
	// 窗口滑过保留期：旧分钟全部过期，只应留下新记录。
	later := base.Add(70 * time.Minute)
	legacy.record(later, time.Second, 200)
	sharded.record(later, time.Second, 200)
	compareSnapshots(t, "过期+新记录", sharded.snapshot(later, 60), legacy.snapshot(later, 60))
	compareSnapshots(t, "过期+新记录(5m)", sharded.snapshot(later.Add(time.Second), 5), legacy.snapshot(later.Add(time.Second), 5))
}

// TestMonitorShardSpreadsRecords 断言分片确实把并发观测摊到多个分片，且归并后的
// 总量与记录次数严格相等（不丢样本、不重复计数）。
// 分片按记录时刻的纳秒低位选（见 monitor.go 的 shardFor）：真实流量每个请求的
// 完成时刻不同，所以这里让时刻逐条递增；同一纳秒内的突发会退化成单分片，那是
// 有意为之的行为，由 TestMonitorShardSameInstantCollapses 单独固定。
func TestMonitorShardSpreadsRecords(t *testing.T) {
	now := time.Now()
	m := newRequestMonitor(now.Add(-time.Minute))
	const workers = 8
	const perWorker = 250
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for j := range perWorker {
				status := 200
				if j%50 == 0 {
					status = 500
				}
				// 每个请求的完成时刻不同：纳秒低位散开，分片才有意义。
				at := now.Add(time.Duration(w*perWorker+j) * time.Nanosecond)
				m.record(at, 3*time.Millisecond, status)
			}
		})
	}
	wg.Wait()
	used, samples := shardUsage(t, m)
	if samples != workers*perWorker {
		t.Fatalf("分片内样本数 %d，期望 %d", samples, workers*perWorker)
	}
	if used < 2 {
		t.Fatalf("观测只落在 %d 个分片上，分片未生效", used)
	}
	got := m.snapshot(now.Add(time.Duration(workers*perWorker)*time.Nanosecond), 5).counts
	if got.Total != workers*perWorker || got.Errors5xx != workers*perWorker/50 {
		t.Fatalf("归并后计数 %+v，期望 total=%d errors5xx=%d", got, workers*perWorker, workers*perWorker/50)
	}
}

// TestMonitorShardSameInstantCollapses 固定「同一纳秒内全部落一个分片」这一退化行为：
// 正确性不受影响（总量照样准），只是并行度退回改造前的单锁水平。
// 之所以不做轮转计数器：那要额外碰一条被所有 P 争用的缓存行，2 核实测把收益吃回去。
func TestMonitorShardSameInstantCollapses(t *testing.T) {
	now := time.Now()
	m := newRequestMonitor(now.Add(-time.Minute))
	const perWorker = 63
	const total = 8 * perWorker
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range perWorker {
				m.record(now, 3*time.Millisecond, 200)
			}
		})
	}
	wg.Wait()
	used, samples := shardUsage(t, m)
	if samples != total {
		t.Fatalf("分片内样本数 %d，期望 %d", samples, total)
	}
	if used != 1 {
		t.Fatalf("同一时刻的记录落在 %d 个分片上，按时间哈希选片应只有 1 个", used)
	}
	if got := m.snapshot(now, 5).counts; got.Total != total {
		t.Fatalf("单分片归并总数 %d，期望 %d", got.Total, total)
	}
}

// shardUsage 统计被写过的分片数与分片内累计样本数。
func shardUsage(t *testing.T, m *requestMonitor) (used int, samples uint64) {
	t.Helper()
	for i := range m.shards {
		shard := &m.shards[i]
		shard.mu.Lock()
		touched := false
		for b := range shard.buckets {
			if shard.buckets[b].initialized {
				samples += shard.buckets[b].total
				touched = true
			}
		}
		shard.mu.Unlock()
		if touched {
			used++
		}
	}
	return used, samples
}

func TestMonitorHistogramBoundaries(t *testing.T) {
	now := time.Now()
	legacy := newLegacyMonitor(now)
	sharded := newRequestMonitor(now)
	elapsed := []time.Duration{
		0, time.Nanosecond, time.Millisecond, time.Millisecond + time.Nanosecond,
		2 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond, 60000 * time.Millisecond,
		60000*time.Millisecond + time.Nanosecond, time.Hour,
	}
	for _, d := range elapsed {
		legacy.record(now, d, 200)
		sharded.record(now, d, 200)
	}
	compareSnapshots(t, "边界", sharded.snapshot(now, 5), legacy.snapshot(now, 5))
	if got := monitorBucketFor(60000*time.Millisecond + time.Nanosecond); got != len(monitorBoundsMS) {
		t.Fatalf("超出上限应落溢出桶 %d，得到 %d", len(monitorBoundsMS), got)
	}
	if got := monitorBucketFor(0); got != 0 {
		t.Fatalf("0 延迟应落第一个桶，得到 %d", got)
	}
	if got := monitorBucketFor(60000 * time.Millisecond); got != len(monitorBoundsMS)-1 {
		t.Fatalf("恰好等于上限应落最后一个有限桶 %d，得到 %d", len(monitorBoundsMS)-1, got)
	}
}

// BenchmarkMonitorRecordLegacy 是改造前单锁实现的对照基准，与仓库既有
// BenchmarkMonitorRecord 用同一负载形状（RunParallel + 17ms/200）。
func BenchmarkMonitorRecordLegacy(b *testing.B) {
	m := newLegacyMonitor(time.Now())
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.record(now, 17*time.Millisecond, 200)
		}
	})
}

// BenchmarkMonitorSnapshotLegacy 同样用于对照改造后的 snapshot（归并成本）。
func BenchmarkMonitorSnapshotLegacy(b *testing.B) {
	m := newLegacyMonitor(time.Now())
	now := time.Now()
	m.record(now, time.Millisecond, 200)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.snapshot(now, 60)
	}
}

// BenchmarkMonitorRecordVaryingLegacy / BenchmarkMonitorRecordVarying 用「每个请求
// 完成时刻不同」的现实负载做 A/B：仓库既有 BenchmarkMonitorRecord 固定同一个
// now，所有 P 撞同一分钟槽，恰好抹掉分片的并行度，只用来做最坏情况对照。
func BenchmarkMonitorRecordVaryingLegacy(b *testing.B) {
	m := newLegacyMonitor(time.Now())
	base := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		at := base
		for pb.Next() {
			at = at.Add(time.Nanosecond)
			m.record(at, 17*time.Millisecond, 200)
		}
	})
}

func BenchmarkMonitorRecordVarying(b *testing.B) {
	m := newRequestMonitor(time.Now())
	base := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		at := base
		for pb.Next() {
			at = at.Add(time.Nanosecond)
			m.record(at, 17*time.Millisecond, 200)
		}
	})
}
