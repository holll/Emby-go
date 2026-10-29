package cache

import (
	"container/list"
	"hash/maphash"
	"sync"
	"time"
)

type Cache interface {
	Get(string) ([]byte, bool)
	Set(string, []byte, time.Duration)
	Delete(string)
	Clear()
}

// entry 是不可变的值单元：任何一次 Set 都会整体替换 Element.Value，
// 已经发布出去的 value 切片终生只读（见 Get 的锁外复制说明）。
type entry struct {
	key     string
	value   []byte
	expires time.Time
}

// maxShards 是分片数上限，slotsPerShard 是每片至少预留的条目数。
//
// 取值理由：
//   - 命中路径的持锁工作只有一次 map 查找 + 链表前移（~100ns 量级），瓶颈是
//     同一把锁上的 cache line 争抢，所以片数只要 ≥ 并发命中数就够。宿主 2 核、
//     列表缓存并发命中通常是个位数，64 片已有 30 倍余量；片数再多在 2 核上
//     拿不到额外吞吐，只增加内存与"配额按片切开"造成的淘汰偏斜。
//   - 每片固定开销 ≈ sync.Mutex(8B) + map 头 + list.List(48B) ≈ 100B，
//     64 片 ≈ 6.5KB/实例；进程内 memory 主缓存(4096)、imgMeta(8192)、
//     imgThumb(512) 三个实例合计不足 20KB，可忽略。
//   - 每片至少 8 个槽位：硬配额下"撞片即淘汰"，若每片只有 1-2 个槽位，小容量
//     实例的有效容量会远低于 max。因此片数按 max/8 收缩，容量越小片数越少
//     （生产容量 4096/8192/512 → 分别 64/64/64 片，每片 64/128/8 个槽位；
//     测试里 max ≤ 16 的实例退化成 1-2 片，等价于旧行为）。
const (
	maxShards     = 64
	slotsPerShard = 8
)

// shardCountFor 返回按容量收缩后的分片数：max/slotsPerShard，夹在 [1, maxShards]。
func shardCountFor(max int) int {
	n := max / slotsPerShard
	if n > maxShards {
		n = maxShards
	}
	if n < 1 {
		n = 1
	}
	return n
}

// shard 是一片独立的 LRU：自己的锁、map、链表与容量配额。
type shard struct {
	mu    sync.Mutex
	cap   int
	items map[string]*list.Element
	order *list.List
}

// Memory 是进程内 LRU 缓存。
//
// 并发模型：key 经 maphash 取模落到固定分片，Get/Set/Delete 只锁单分片，
// 且把 O(size) 的 []byte 克隆移到锁外 —— 命中路径的临界区与值大小无关。
//
// 安全前提（Get 锁外复制成立的条件，改动时务必维持）：
//  1. 存进去的切片在发布之后永不被写：Set 存的是入参的私有副本（cp 在锁外
//     构造、之后只读发布），调用方之后改自己的切片不影响缓存；SetBytes 存的
//     就是入参本身，其调用方承诺交出后不再改动（所有权转移）；
//  2. 库内没有任何 API 返回内部切片本身：Get 返回独立副本；
//  3. 淘汰/TTL 过期只会 delete+Remove 整个 Element，从不原地修改 value。
//
// 因此 Get 在锁内取到的 v.value 在解锁后仍是不可变且被引用的切片，
// 可在锁外安全复制；期间同 key 的 Set/Delete 只会替换或摘除 Element，
// 不会改写我们已经拿到的底层数组。
//
// 容量语义：总条目数仍然严格 ≤ max（与单锁版本一致，不做"允许轻微超额
// 再靠原子计数收敛"的方案 —— 那会让 len(items) > max 的瞬态可被并发观察，
// 也会给每次 Set 多加一次原子操作）。做法是把 max 精确切给各分片
// （sum(cap) == max，余数分给前 max%n 片），每片独立做 LRU 淘汰。
// 代价是 LRU 变成分片局部近似：某片配额打满而其它片还有空位时，会淘汰该片
// 内相对较热的条目；写得接近写满时，有效可用容量约为 max 的 90-95%
// （分片 LRU 的标准代价），但任何时刻都不会超过 max。
//
// 被否决的替代方案：用全局 atomic 计数在 len > max 时收敛淘汰。它能让单片刻度
// 超过 max/n（没有提前淘汰），但 (a) 每次 Set 多一次原子操作，(b) 收敛只能淘汰
// 当前分片的尾部，其它分片仍满时会退化成"插入即自淘汰"，(c) len(items) > max
// 的瞬态可被并发观察，属于隐性行为变化。故选择精确配额切分。
type Memory struct {
	shards []shard
	seed   maphash.Seed
	max    int
}

func NewMemory(max int) *Memory {
	if max < 1 {
		max = 256
	}
	n := shardCountFor(max)
	m := &Memory{seed: maphash.MakeSeed(), max: max, shards: make([]shard, n)}
	// max 精确切分：前 rem 片各多拿 1 个槽位，保证 sum(cap) == max，
	// 总条目数上限与单锁版本完全一致，不存在"偶尔超出 max"的隐性变化。
	base, rem := max/n, max%n
	for i := range m.shards {
		c := base
		if i < rem {
			c++
		}
		m.shards[i] = shard{cap: c, items: make(map[string]*list.Element, c), order: list.New()}
	}
	return m
}

// shardFor 用带随机种子的 maphash 对 key 取模选片：无分配、并发安全，
// 且种子每实例随机，外部构造的 key 无法刻意聚到同一片（FNV-1a 之类
// 无种子哈希可以被请求参数里的 term/genre 等字段诱发聚集）。
func (m *Memory) shardFor(key string) *shard {
	if len(m.shards) == 1 {
		return &m.shards[0]
	}
	return &m.shards[maphash.String(m.seed, key)%uint64(len(m.shards))]
}

func (m *Memory) Get(key string) ([]byte, bool) {
	s := m.shardFor(key)
	s.mu.Lock()
	e, ok := s.items[key]
	if !ok {
		s.mu.Unlock()
		return nil, false
	}
	v := e.Value.(entry)
	if !v.expires.IsZero() && time.Now().After(v.expires) {
		delete(s.items, key)
		s.order.Remove(e)
		s.mu.Unlock()
		return nil, false
	}
	s.order.MoveToFront(e)
	s.mu.Unlock()
	// 复制在锁外进行：600KB 列表 DTO 的克隆不再串行化整个缓存。
	// v.value 在解锁后依然有效且不可变（见 Memory 文档的安全前提）。
	return append([]byte(nil), v.value...), true
}

// Viewer 是缓存后端的可选能力：返回内部持有的只读视图，而不是副本。
//
// 只有进程内的 Memory 能实现（Redis 后端跨网络，视图没有意义）。调用方在
// 拿不到 Viewer 时应退回 Get。
type Viewer interface {
	View(string) ([]byte, bool)
}

// View 是 Get 的零拷贝版本：返回缓存自己持有的切片，不克隆。
//
// 契约比 Get 严格，三条都必须遵守：
//  1. 返回值只读：不得改写、不得 append、不得再切片到 len 以外；
//  2. 内容在本次调用后保持不变——缓存只会整体替换条目（store 写新的
//     Element.Value），淘汰与 TTL 只摘链表节点，从不原地改写已发布的值；
//  3. 有效期到同 key 的下一次 Set/SetBytes/Delete/淘汰/超时为止，典型用法是
//     "立刻写进 HTTP 响应"。
//
// 为什么值得单开一个入口：列表 DTO 一页 200 条约 600KB，Get 每次命中都要克隆
// 一整份（614,400 B/op、~130µs），而响应体只是被写进 socket 一次
// （BenchmarkViewHit600K：0 B/op、~100ns）。需要持有或改写副本的调用方继续用 Get。
func (m *Memory) View(key string) ([]byte, bool) {
	s := m.shardFor(key)
	s.mu.Lock()
	e, ok := s.items[key]
	if !ok {
		s.mu.Unlock()
		return nil, false
	}
	v := e.Value.(entry)
	if !v.expires.IsZero() && time.Now().After(v.expires) {
		delete(s.items, key)
		s.order.Remove(e)
		s.mu.Unlock()
		return nil, false
	}
	s.order.MoveToFront(e)
	s.mu.Unlock()
	return v.value, true
}

func (m *Memory) Set(key string, value []byte, ttl time.Duration) {
	// 入参的私有副本在锁外构造，调用方 Set 之后仍可随意复用/改写 value。
	m.store(key, append([]byte(nil), value...), ttl)
}

// SetBytes 与 Set 只有一点不同：它**接管** value 的所有权，不再复制一份。
//
// 调用方交出 value 之后不得再读写或复用它的底层数组；库内保证只读地持有它
// （Get 仍然复制出去，淘汰/TTL 只摘链表节点，从不原地改写）。
//
// 为什么需要这个入口：列表页 DTO 一页 200 条约 600KB，响应体是 json.Marshal
// 刚生成的、之后只用于写响应的一次性字节。Set 的防御性复制会让每个列表响应
// 多分配并拷贝一份同尺寸内存（BenchmarkSet600K：614,464 B/op、2 allocs/op；
// BenchmarkSetBytes600K：≈24 B/op、1 alloc/op），在 15 秒 TTL 的列表缓存上
// 等于把一次响应体的分配翻倍。
//
// 契约边界（与 Set 的差别仅此一条）：Set 之后调用方可以随意改自己的切片；
// SetBytes 之后不可以。其余全部一致：Get/View 都不会原地改写值、TTL、分片
// 淘汰、容量上限、nil/空值语义；区别只在 Get 返回副本、View 返回只读视图。
func (m *Memory) SetBytes(key string, value []byte, ttl time.Duration) {
	m.store(key, value, ttl)
}

// store 是 Set/SetBytes 的共同插入路径。value 必须已经是"归缓存所有的只读切片"。
func (m *Memory) store(key string, value []byte, ttl time.Duration) {
	cp := value
	s := m.shardFor(key)
	s.mu.Lock()
	// 过期时间在锁内、拷贝之后计算，与单锁版本"写入时刻开始计时"一致。
	exp := expiry(ttl)
	if e, ok := s.items[key]; ok {
		e.Value = entry{key: key, value: cp, expires: exp}
		s.order.MoveToFront(e)
		s.mu.Unlock()
		return
	}
	e := s.order.PushFront(entry{key: key, value: cp, expires: exp})
	s.items[key] = e
	if len(s.items) > s.cap {
		if old := s.order.Back(); old != nil {
			delete(s.items, old.Value.(entry).key)
			s.order.Remove(old)
		}
	}
	s.mu.Unlock()
}

func expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func (m *Memory) Delete(key string) {
	s := m.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.items[key]; ok {
		delete(s.items, key)
		s.order.Remove(e)
	}
}

// Clear 逐片清空。分片后 Clear 不再是一次全局原子操作：并发的 Get 可能从
// 还没被清理到的分片命中（旧实现在全局锁下不可能观察到此中间态）。
// 语义影响：Clear 之后仍可能短暂返回旧值，调用方（库内只有运维/设置变更
// 路径）不依赖"Clear 与 Get 的严格顺序"，故可接受；已在注释与报告中声明。
func (m *Memory) Clear() {
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		s.items = make(map[string]*list.Element, s.cap)
		s.order.Init()
		s.mu.Unlock()
	}
}
