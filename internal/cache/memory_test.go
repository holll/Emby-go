package cache

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// countEntries 汇总所有分片的条目数，用于校验全局容量上限。
func (m *Memory) countEntries() int {
	total := 0
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		if len(s.items) != s.order.Len() {
			total = -1 // map 与链表不一致，交给断言报错
		}
		total += len(s.items)
		s.mu.Unlock()
	}
	return total
}

func TestMemoryShardCapSumsToMax(t *testing.T) {
	for _, max := range []int{1, 2, 3, 7, 63, 64, 65, 100, 256, 4096, 8192} {
		m := NewMemory(max)
		sum := 0
		for i := range m.shards {
			if m.shards[i].cap < 1 {
				t.Fatalf("max=%d shard %d has cap %d", max, i, m.shards[i].cap)
			}
			sum += m.shards[i].cap
		}
		if sum != max {
			t.Fatalf("max=%d: sum(cap)=%d, want %d", max, sum, max)
		}
		if got, want := len(m.shards), shardCountFor(max); got != want {
			t.Fatalf("max=%d: %d shards, want %d", max, got, want)
		}
		// max >= 4 时每片至少 4 个槽位，避免撞片即互相淘汰。
		if max >= slotsPerShard && len(m.shards) == maxShards {
			if base := max / len(m.shards); base < slotsPerShard {
				t.Fatalf("max=%d: shard base cap %d < %d", max, base, slotsPerShard)
			}
		}
		if m.max != max {
			t.Fatalf("max=%d: m.max=%d", max, m.max)
		}
	}
	// 非法容量仍按旧行为回落到 256。
	if m := NewMemory(0); m.max != 256 || m.countEntries() != 0 {
		t.Fatalf("NewMemory(0) = max %d", m.max)
	}
	if m := NewMemory(-5); m.max != 256 {
		t.Fatalf("NewMemory(-5) = max %d", m.max)
	}
}

// TestMemoryGetReturnsIndependentCopy 校验「Get 返回独立副本、Set 复制入参」。
// 这是克隆移到锁外后仍必须成立的核心契约。
func TestMemoryGetReturnsIndependentCopy(t *testing.T) {
	c := NewMemory(16)
	in := []byte("payload")
	c.Set("k", in, time.Hour)

	// 调用方改自己的入参不得影响缓存。
	in[0] = 'X'
	if got, ok := c.Get("k"); !ok || string(got) != "payload" {
		t.Fatalf("cache aliased Set input: %q ok=%v", got, ok)
	}

	first, ok := c.Get("k")
	if !ok {
		t.Fatal("miss after Set")
	}
	first[0] = 'Y'
	second, ok := c.Get("k")
	if !ok || string(second) != "payload" {
		t.Fatalf("cache aliased Get result: %q", second)
	}
	if &first[0] == &second[0] {
		t.Fatal("two Gets returned the same backing array")
	}
}

func TestMemoryEvictionAndTTL(t *testing.T) {
	c := NewMemory(1)
	c.Set("a", []byte("one"), time.Hour)
	if got, ok := c.Get("a"); !ok || string(got) != "one" {
		t.Fatal("memory cache miss")
	}
	c.Set("b", []byte("two"), time.Hour)
	if _, ok := c.Get("a"); ok {
		t.Fatal("LRU did not evict")
	}
	c.Set("c", []byte("three"), time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("c"); ok {
		t.Fatal("TTL did not expire")
	}
	// ttl <= 0 表示永不过期。
	c.Set("d", []byte("forever"), 0)
	time.Sleep(2 * time.Millisecond)
	if got, ok := c.Get("d"); !ok || string(got) != "forever" {
		t.Fatal("zero ttl expired")
	}
	// nil / 空值语义：nil 值也算命中，返回的是空的独立副本。
	c.Set("nil", nil, time.Hour)
	if got, ok := c.Get("nil"); !ok || len(got) != 0 {
		t.Fatalf("nil value: %q ok=%v", got, ok)
	}
}

// TestMemoryCapacityBoundAcrossShards 校验分片后总条目数仍严格 ≤ max。
func TestMemoryCapacityBoundAcrossShards(t *testing.T) {
	for _, max := range []int{1, 3, 64, 256} {
		c := NewMemory(max)
		for i := 0; i < max*8; i++ {
			c.Set(fmt.Sprintf("key-%d", i), []byte(fmt.Sprintf("value-%d", i)), time.Hour)
			if n := c.countEntries(); n < 0 {
				t.Fatalf("max=%d: map/order out of sync", max)
			} else if n > max {
				t.Fatalf("max=%d: %d entries exceed capacity", max, n)
			}
		}
		// 容量写满后仍应命中最近写入的 key。
		last := fmt.Sprintf("key-%d", max*8-1)
		if got, ok := c.Get(last); !ok || string(got) != fmt.Sprintf("value-%d", max*8-1) {
			t.Fatalf("max=%d: most recent key missing: %q ok=%v", max, got, ok)
		}
		// 大量写入后仍能继续工作。
		c.Set("post-evict", []byte("v"), time.Hour)
		if got, ok := c.Get("post-evict"); !ok || string(got) != "v" {
			t.Fatalf("max=%d: cache unusable after full eviction", max)
		}
	}
}

func TestMemoryDeleteAndClear(t *testing.T) {
	c := NewMemory(128)
	for i := 0; i < 100; i++ {
		c.Set(fmt.Sprintf("k%d", i), []byte("v"), time.Hour)
	}
	c.Delete("k42")
	if _, ok := c.Get("k42"); ok {
		t.Fatal("Delete did not remove entry")
	}
	c.Delete("missing") // 不存在的 key 不得 panic
	c.Clear()
	if n := c.countEntries(); n != 0 {
		t.Fatalf("Clear left %d entries", n)
	}
	if _, ok := c.Get("k0"); ok {
		t.Fatal("Get hit after Clear")
	}
	c.Set("after", []byte("v2"), time.Hour)
	if got, ok := c.Get("after"); !ok || string(got) != "v2" {
		t.Fatal("cache unusable after Clear")
	}
}

func TestMemoryShardForKeyIsStable(t *testing.T) {
	c := NewMemory(64)
	for _, key := range []string{"", "items:abc", "entities:scope-v1:1:Movie", "k"} {
		first := c.shardFor(key)
		for i := 0; i < 8; i++ {
			if got := c.shardFor(key); got != first {
				t.Fatalf("key %q mapped to different shards", key)
			}
		}
	}
}

// TestMemoryConcurrentAccess 用 -race 覆盖分片锁与锁外复制：
// 一边并发 Get，一边并发 Set 覆盖同一批 key，校验返回值永远是完整副本
// （不会读到被替换/正在复制的半成品）。
func TestMemoryConcurrentAccess(t *testing.T) {
	const (
		keys    = 16
		size    = 4096
		writers = 4
		readers = 8
		rounds  = 200
	)
	c := NewMemory(32)
	bodies := make([][]byte, keys)
	for i := range bodies {
		bodies[i] = bytes.Repeat([]byte{byte('a' + i%26)}, size)
		c.Set(fmt.Sprintf("k%d", i), bodies[i], time.Hour)
	}

	var writersWG, readersWG sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < writers; w++ {
		writersWG.Add(1)
		go func(w int) {
			defer writersWG.Done()
			for r := 0; r < rounds; r++ {
				k := (w*7 + r) % keys
				c.Set(fmt.Sprintf("k%d", k), bodies[k], time.Hour)
				if r%17 == 0 {
					c.Delete(fmt.Sprintf("k%d", (k+1)%keys))
				}
				if r%53 == 0 {
					c.Clear()
				}
				c.Set(fmt.Sprintf("k%d", k), bodies[k], time.Hour)
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for i := 0; i < keys; i++ {
					v, ok := c.Get(fmt.Sprintf("k%d", i))
					if !ok {
						continue
					}
					if len(v) != size {
						t.Errorf("torn value: len %d", len(v))
						return
					}
					if v[0] != v[size-1] {
						t.Errorf("torn value: %q..%q", v[0], v[size-1])
						return
					}
				}
			}
		}()
	}
	// 写者跑完后停止读者，再等读者退出。
	writersWG.Wait()
	close(stop)
	readersWG.Wait()
	if n := c.countEntries(); n < 0 {
		t.Fatal("map/order out of sync after concurrent access")
	}
}

// storedValue 白盒取回某个 key 当前被缓存持有的底层切片（仅测试用）。
func storedValue(t *testing.T, m *Memory, key string) []byte {
	t.Helper()
	s := m.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[key]
	if !ok {
		t.Fatalf("key %q missing", key)
	}
	return e.Value.(entry).value
}

// TestMemorySetCopiesInput 守住 Set 的既有契约：存入的是私有副本，
// 调用方在 Set 之后改自己的切片不能影响缓存内容（16 处调用方依赖这一点）。
func TestMemorySetCopiesInput(t *testing.T) {
	c := NewMemory(64)
	body := []byte(`{"Items":[1,2,3]}`)
	want := append([]byte(nil), body...)
	c.Set("k", body, time.Minute)

	if stored := storedValue(t, c, "k"); unsafe.SliceData(stored) == unsafe.SliceData(body) {
		t.Fatal("Set must store a private copy, not the caller's slice")
	}
	for i := range body {
		body[i] = 'x'
	}
	got, ok := c.Get("k")
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("cache content changed after caller mutated its input: %q", got)
	}
}

// TestMemorySetBytesTransfersOwnership 是 P2-5 的核心断言：
// SetBytes 之后缓存持有的就是调用方交出的那块内存（同一底层数组），
// 即列表响应省掉了"marshal 一次 + Set 再复制一次"里的第二份拷贝；
// 同时 Get 仍然返回独立副本，语义与 Set 一致。
func TestMemorySetBytesTransfersOwnership(t *testing.T) {
	c := NewMemory(64)
	body := []byte(`{"Items":[]}`)
	c.SetBytes("k", body, time.Minute)

	stored := storedValue(t, c, "k")
	if unsafe.SliceData(stored) != unsafe.SliceData(body) {
		t.Fatal("SetBytes must adopt the caller's slice (ownership transfer), but a copy was made")
	}
	if len(stored) != len(body) {
		t.Fatalf("stored length = %d, want %d", len(stored), len(body))
	}
	got, ok := c.Get("k")
	if !ok || !bytes.Equal(got, body) {
		t.Fatalf("Get after SetBytes = %q, want %q", got, body)
	}
	if unsafe.SliceData(got) == unsafe.SliceData(body) {
		t.Fatal("Get must keep returning an independent copy even for SetBytes entries")
	}
	// 改 Get 返回的副本不得影响缓存（所有权在缓存手里）。
	got[0] = 'X'
	again, _ := c.Get("k")
	if !bytes.Equal(again, body) {
		t.Fatalf("mutating the returned copy leaked into the cache: %q", again)
	}
}

// TestMemorySetBytesReplacementDoesNotRewriteOldBytes 守住"已发布字节不被后续写入改写"：
// 同 key 再写一次只会替换链表节点（新数组），先前 Get 出去/交出去的那份内存
// 内容必须保持原样 —— 服务端正是把这块内存同时交给缓存和响应写入。
func TestMemorySetBytesReplacementDoesNotRewriteOldBytes(t *testing.T) {
	c := NewMemory(64)
	first := []byte("first-response-body")
	c.SetBytes("k", first, time.Minute)
	handed, _ := c.Get("k") // 相当于写响应用的那份字节

	second := []byte("second-response-body")
	c.SetBytes("k", second, time.Minute)
	c.Set("k2", []byte("x"), time.Minute)
	c.Delete("k2")

	if !bytes.Equal(handed, []byte("first-response-body")) {
		t.Fatalf("previously handed-out bytes were rewritten: %q", handed)
	}
	if got, _ := c.Get("k"); !bytes.Equal(got, second) {
		t.Fatalf("new value not visible: %q", got)
	}
	// 同 key 的 Set（复制路径）与 SetBytes 必须能互相覆盖，且不共享内存。
	c.Set("k", second, time.Minute)
	if got, _ := c.Get("k"); !bytes.Equal(got, second) {
		t.Fatalf("Set after SetBytes lost the value: %q", got)
	}
}

// TestMemorySetBytesConcurrentGetStable 在 -race 下并发读同一个 SetBytes 条目，
// 每次读都必须拿到完整、未撕裂的内容（缓存对这块内存只读）。
func TestMemorySetBytesConcurrentGetStable(t *testing.T) {
	c := NewMemory(64)
	body := make([]byte, 64*1024)
	for i := range body {
		body[i] = byte(i * 7)
	}
	h := fnv.New64a()
	_, _ = h.Write(body)
	want := h.Sum64()
	c.SetBytes("items:k", body, time.Minute)

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for range 50 {
				got, ok := c.Get("items:k")
				if !ok {
					t.Errorf("goroutine %d: key missing", g)
					return
				}
				h := fnv.New64a()
				_, _ = h.Write(got)
				if h.Sum64() != want {
					t.Errorf("goroutine %d: content torn", g)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
