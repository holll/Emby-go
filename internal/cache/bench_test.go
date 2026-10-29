package cache

import (
	"container/list"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// benchValueBytes 取 600KB，对应 review 里实测的 600,000 B 量级列表 DTO
// （614,400 B = 600 KiB，与 docs/2026-10-29-go-review.md §2.2 P0-2 口径一致）。
const benchValueBytes = 600 * 1024

var benchSink int

func benchPayload() []byte {
	v := make([]byte, benchValueBytes)
	for i := range v {
		v[i] = byte(i * 31)
	}
	return v
}

// ---------- 改造后：分片锁 + 锁外复制 ----------

// BenchmarkGetHit600K 命中路径：持锁只有一次 map 查找 + 链表前移，
// 600KB 克隆在锁外完成。
func BenchmarkGetHit600K(b *testing.B) {
	c := NewMemory(64)
	value := benchPayload()
	c.Set("items:bench", value, time.Hour)
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, ok := c.Get("items:bench")
		if !ok {
			b.Fatal("bench key missing")
		}
		benchSink = len(v)
	}
}

// BenchmarkSet600K 写入路径：入参私有副本在锁外构造，锁内只替换 Element。
func BenchmarkSet600K(b *testing.B) {
	c := NewMemory(64)
	value := benchPayload()
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set("items:bench", value, time.Hour)
	}
	benchSink = len(value)
}

// BenchmarkSetBytes600K 是服务端列表响应实际走的写入路径（P2-5）：
// 响应体是 json.Marshal 刚产出的、之后只用于写响应的一次性字节，
// 交给缓存时不需要再复制。与 BenchmarkSet600K 的差值就是省掉的那一份副本。
func BenchmarkSetBytes600K(b *testing.B) {
	c := NewMemory(64)
	value := benchPayload()
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.SetBytes("items:bench", value, time.Hour)
	}
	benchSink = len(value)
}

// BenchmarkParallelGetHit 用 GOMAXPROCS 个 goroutine 并发命中同一个 600KB
// 条目：分片锁下各 goroutine 只在锁内做 O(1) 工作，克隆并行进行。
func BenchmarkParallelGetHit(b *testing.B) {
	c := NewMemory(64)
	c.Set("items:bench", benchPayload(), time.Hour)
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	var local int
	b.RunParallel(func(pb *testing.PB) {
		n := 0
		for pb.Next() {
			v, ok := c.Get("items:bench")
			if !ok {
				b.Error("bench key missing")
				return
			}
			n += len(v)
		}
		local = n
	})
	benchSink = local
	runtime.KeepAlive(c)
}

// ---------- 改造前基线 ----------
//
// benchLegacyMemory 是改造前 internal/cache.Memory 的逐字副本（单把全局
// sync.Mutex + 锁内 append 整值克隆 + 全局 LRU 上限），仅用于在同一台机器、
// 同一次 bench 运行里给出可信的前后对比数字。生产代码不使用它。

type benchLegacyMemory struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element
	order *list.List
}

type benchLegacyEntry struct {
	key     string
	value   []byte
	expires time.Time
}

func newBenchLegacyMemory(max int) *benchLegacyMemory {
	if max < 1 {
		max = 256
	}
	return &benchLegacyMemory{max: max, items: map[string]*list.Element{}, order: list.New()}
}

func (m *benchLegacyMemory) Get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return nil, false
	}
	v := e.Value.(benchLegacyEntry)
	if !v.expires.IsZero() && time.Now().After(v.expires) {
		delete(m.items, key)
		m.order.Remove(e)
		return nil, false
	}
	m.order.MoveToFront(e)
	return append([]byte(nil), v.value...), true
}

func (m *benchLegacyMemory) Set(key string, value []byte, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	if e, ok := m.items[key]; ok {
		e.Value = benchLegacyEntry{key: key, value: append([]byte(nil), value...), expires: exp}
		m.order.MoveToFront(e)
		return
	}
	e := m.order.PushFront(benchLegacyEntry{key: key, value: append([]byte(nil), value...), expires: exp})
	m.items[key] = e
	if len(m.items) > m.max {
		old := m.order.Back()
		delete(m.items, old.Value.(benchLegacyEntry).key)
		m.order.Remove(old)
	}
}

func BenchmarkLegacyGetHit600K(b *testing.B) {
	c := newBenchLegacyMemory(64)
	c.Set("items:bench", benchPayload(), time.Hour)
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, ok := c.Get("items:bench")
		if !ok {
			b.Fatal("bench key missing")
		}
		benchSink = len(v)
	}
}

func BenchmarkLegacySet600K(b *testing.B) {
	c := newBenchLegacyMemory(64)
	value := benchPayload()
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set("items:bench", value, time.Hour)
	}
	benchSink = len(value)
}

func BenchmarkParallelLegacyGetHit(b *testing.B) {
	c := newBenchLegacyMemory(64)
	c.Set("items:bench", benchPayload(), time.Hour)
	b.SetBytes(benchValueBytes)
	b.ReportAllocs()
	b.ResetTimer()
	var local int
	b.RunParallel(func(pb *testing.PB) {
		n := 0
		for pb.Next() {
			v, ok := c.Get("items:bench")
			if !ok {
				b.Error("bench key missing")
				return
			}
			n += len(v)
		}
		local = n
	})
	benchSink = local
	runtime.KeepAlive(c)
}

// benchStore 让新实现与基线实现走同一段混合负载代码。
type benchStore interface {
	Get(string) ([]byte, bool)
	Set(string, []byte, time.Duration)
}

const (
	benchSmallKeys  = 64
	benchSmallBytes = 1024
)

// benchMixedBigHitSmallGets 量化 review 结论里的真实病灶：单把全局锁 +
// 锁内 O(size) 克隆时，一次 600KB 命中会把所有小值的命中一起挡住。
// 这里一个后台 goroutine 持续命中 600KB 条目，测量前台小值命中的 ns/op：
// 分片锁下两者落在不同分片（且克隆在锁外），前台不再等后台的拷贝。
func benchMixedBigHitSmallGets(b *testing.B, c benchStore) {
	src := make([]byte, benchSmallBytes)
	keys := make([]string, benchSmallKeys)
	for i := 0; i < benchSmallKeys; i++ {
		keys[i] = "small:" + strconv.Itoa(i)
		c.Set(keys[i], src, time.Hour)
	}
	c.Set("big:bench", benchPayload(), time.Hour)

	stop := make(chan struct{})
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if v, ok := c.Get("big:bench"); ok {
				benchSink = len(v)
			}
		}
	}()

	b.SetBytes(benchSmallBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, ok := c.Get(keys[i%benchSmallKeys])
		if !ok {
			b.Fatal("bench key missing")
		}
		benchSink = len(v)
	}
	b.StopTimer()
	close(stop)
	bg.Wait()
}

func BenchmarkMixedBigHitSmallGets(b *testing.B) {
	// 用生产容量（defaultMemoryCacheEntries 量级）：硬配额下分片数按 max 收缩，
	// 只有接近写满时才会有撞片淘汰，这里 65 个 key 远低于上限。
	benchMixedBigHitSmallGets(b, NewMemory(4096))
}

func BenchmarkLegacyMixedBigHitSmallGets(b *testing.B) {
	benchMixedBigHitSmallGets(b, newBenchLegacyMemory(4096))
}
