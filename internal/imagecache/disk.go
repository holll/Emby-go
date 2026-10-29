// Package imagecache 把按请求尺寸生成的缩略图变体落到磁盘。
//
// 缩略图的成本几乎都在 CPU（webp 解码 + 缩放 + 编码，单张 45~130ms），
// 而客户端的尺寸需求是离散且重复的：同一部影片的同一张海报，今天缩成
// 200 宽、明天还是 200 宽。落盘后重启进程、内存 LRU 被新条目顶掉都不再
// 需要重新解码，磁盘只受容量上限和保留时长约束。
package imagecache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// variantExt 是变体的扩展名：编码器固定输出 webp，扩展名只是给人看的。
	variantExt = ".webp"
	// tmpPrefix 是原子写入的临时文件名前缀。
	tmpPrefix = ".tmp-"
	// touchInterval 限定命中时刷新 mtime 的频率。mtime 是淘汰时唯一的
	// 「最近使用」证据，但每次命中都写一次元数据不值得，一小时一次足够
	// 把仍在用的变体留在淘汰候选的尾部。
	touchInterval = time.Hour
	// trimInterval 是后台清理的最小间隔，避免每写一个变体就 walk 一遍目录。
	trimInterval = 10 * time.Minute
	// trimTargetPercent 是超限后清理到的水位：留出余量，避免刚好踩线时
	// 每个新变体都触发一次清理。
	trimTargetPercent = 80
	// staleTmpAge 超过这个年龄的临时文件视为上次写入崩溃的残留。
	staleTmpAge = time.Hour
)

// Disk 是一个按 hash 分两级的变体缓存目录。
//
// 未启用时 Get 恒未命中、Put 是空操作，调用方不需要为「缓存没开」写分支；
// 这正是让磁盘缓存可以随时关掉而图片接口行为不变的前提。
type Disk struct {
	dir      string
	maxBytes int64
	ttl      time.Duration
	enabled  bool

	trimMu   sync.Mutex
	lastTrim time.Time
	trimming atomic.Bool
	// walkMu 串行化整段目录 walk（P2-3）：Trim 全程持有它，因此并发的
	// Trim/MaybeTrim 不会同时扫同一目录、不会重复计数、也不会互相覆盖 lastTrim。
	// 它是独立于 trimMu 的一把锁——trimMu 只保护 maxBytes/ttl/lastTrim 的短临界区，
	// 而 Trim 内部要通过 recordTrim 写 lastTrim，合并成一把锁会自死锁。
	walkMu sync.Mutex
}

// Open 打开（必要时创建）变体目录。
// enabled=false 或 dir 为空时返回一个禁用状态的缓存；其余错误由调用方
// 决定是否降级——缓存不可用不该让图片接口失败。
func Open(dir string, maxBytes int64, ttl time.Duration, enabled bool) (*Disk, error) {
	if !enabled || strings.TrimSpace(dir) == "" {
		return &Disk{}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建缩略图缓存目录 %q: %w", dir, err)
	}
	return &Disk{dir: dir, maxBytes: maxBytes, ttl: ttl, enabled: true}, nil
}

// Disabled 返回一个禁用状态的缓存，用于缓存目录创建失败时的降级。
func Disabled() *Disk { return &Disk{} }

// Stats 是变体目录当前占用的快照（不含临时文件）。
type Stats struct {
	Files int
	Bytes int64
}

// Stats 统计变体数量与总字节数。设置页展示用；目录很大时耗时随文件数线性增长。
func (d *Disk) Stats() (Stats, error) {
	var st Stats
	if !d.Enabled() {
		return st, nil
	}
	err := filepath.WalkDir(d.dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 与 Trim 一致：扫不到的目录不阻断统计
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), tmpPrefix) || !strings.HasSuffix(entry.Name(), variantExt) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		st.Files++
		st.Bytes += info.Size()
		return nil
	})
	return st, err
}

// SetMaxBytes 在线调整容量上限（0 表示不限制），下一次 Trim 生效。
// 与 MaybeTrim/Trim 并发安全：容量是唯一可能被设置页改写的字段。
func (d *Disk) SetMaxBytes(maxBytes int64) {
	if d == nil {
		return
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	d.trimMu.Lock()
	d.maxBytes = maxBytes
	d.trimMu.Unlock()
}

// MaxBytes 返回当前容量上限（0 表示不限制）。
func (d *Disk) MaxBytes() int64 {
	if d == nil {
		return 0
	}
	d.trimMu.Lock()
	defer d.trimMu.Unlock()
	return d.maxBytes
}

// Enabled 报告缓存是否可用；对 nil 接收者返回 false。
func (d *Disk) Enabled() bool { return d != nil && d.enabled }

// Dir 返回缓存目录（禁用时为空串），主要供日志与测试使用。
func (d *Disk) Dir() string {
	if d == nil {
		return ""
	}
	return d.dir
}

// PathFor 返回 key 对应的变体路径，禁用时为空串；仅供测试断言。
func (d *Disk) PathFor(key string) string {
	if !d.Enabled() {
		return ""
	}
	return d.path(key)
}

// path 把 key 映射成 dir/<两位 hex>/<32 位 hex>.webp。
// 用哈希而不是把路径拼进来：文件名长度稳定，也不会因为路径里的特殊字符
// 或超长目录名出问题；分两级是为了避免单目录几万个文件。
func (d *Disk) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:16])
	return filepath.Join(d.dir, name[:2], name+variantExt)
}

// Get 读取变体字节；未命中、禁用、读失败都返回 false（读失败等价于没缓存）。
func (d *Disk) Get(key string) ([]byte, bool) {
	if !d.Enabled() {
		return nil, false
	}
	path := d.path(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	d.touch(path)
	return data, true
}

// touch 在命中时把 mtime 往现在推，作为「最近使用」的近似记录。
// 失败无所谓：最多是这个变体在淘汰时显得更旧。
func (d *Disk) touch(path string) {
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) < touchInterval {
		return
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// Put 写入变体。先写同目录临时文件再 rename：并发读者要么看到旧内容，
// 要么看到完整的新内容，不会读到半个文件。失败返回错误，但调用方通常
// 只记录日志——缓存写不进去不影响这次响应。
func (d *Disk) Put(key string, data []byte) error {
	if !d.Enabled() || len(data) == 0 {
		return nil
	}
	path := d.path(key)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // rename 成功后是 no-op
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d.MaybeTrim()
	return nil
}

// variant 是 Trim 扫描到的一个变体文件。
type variant struct {
	path  string
	size  int64
	mtime time.Time
}

// Trim 删掉过期变体，并在超限时按 mtime 从旧到新淘汰到 80% 水位。
// 返回删除数量与释放字节数，供日志与测试使用。
//
// 并发（P2-3）：整段 walk 由 walkMu 串行化。此前 walk 在锁外跑，设置页保存 /
// 启动清理 / 管理端点的直接 Trim() 会与 MaybeTrim 起的后台 trim 并发扫同一目录，
// 造成重复删除计数、lastTrim 互相覆盖使十分钟节流失真（不损坏文件）。
// 这里没把 walk 塞进 trimMu：Trim 末尾要 recordTrim 写 lastTrim，同锁自死锁。
// 也没改成「单 owner goroutine + 请求 channel」：Trim 必须同步返回 removed/freed
// （设置页与启动清理都要用），owner 会带来常驻 goroutine 的退出语义，
// 而 walkMu 已经达到「同一时刻只有一路 walk」的效果，且不新增生命周期。
func (d *Disk) Trim() (removed int, freed int64, err error) {
	if !d.Enabled() {
		return 0, 0, nil
	}
	d.walkMu.Lock()
	defer d.walkMu.Unlock()
	d.trimMu.Lock()
	maxBytes, ttl := d.maxBytes, d.ttl
	d.trimMu.Unlock()

	now := time.Now()
	var items []variant
	var total int64
	walkErr := filepath.WalkDir(d.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // 目录正在被删/权限问题不阻断清理
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(entry.Name(), tmpPrefix) {
			// 上次写入崩溃留下的临时文件，过期就清掉。
			if now.Sub(info.ModTime()) > staleTmpAge {
				if os.Remove(path) == nil {
					removed++
					freed += info.Size()
				}
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), variantExt) {
			return nil
		}
		items = append(items, variant{path: path, size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		return removed, freed, walkErr
	}
	keep := make([]variant, 0, len(items))
	for _, item := range items {
		if ttl > 0 && now.Sub(item.mtime) > ttl {
			if os.Remove(item.path) == nil {
				removed++
				freed += item.size
				total -= item.size
			}
			continue
		}
		keep = append(keep, item)
	}
	// 淘汰阶段重读上限：walk 期间设置页可能已经把上限调小，用新值再判一次，
	// 不必为此再走一遍目录。
	d.trimMu.Lock()
	if d.maxBytes > 0 {
		maxBytes = d.maxBytes
	}
	d.trimMu.Unlock()
	if maxBytes > 0 && total > maxBytes {
		target := maxBytes * trimTargetPercent / 100
		slices.SortFunc(keep, func(left, right variant) int {
			return left.mtime.Compare(right.mtime)
		})
		for _, item := range keep {
			if total <= target {
				break
			}
			if os.Remove(item.path) == nil {
				removed++
				freed += item.size
				total -= item.size
			}
		}
	}
	d.recordTrim()
	return removed, freed, nil
}

// BeginTrim 申请「本进程唯一的一路清理」令牌，返回的 release 必须调用（defer）。
// ok=false 表示已有一路清理在跑：调用方应跳过本次——那一路会把目录扫完。
//
// 与 MaybeTrim 共用同一个 trimming CAS，因此手动 Trim（设置页保存、启动清理、
// 管理端点）与后台 trim 不会同时 walk（P2-1：连点保存 N 次只会有一次真在扫）。
func (d *Disk) BeginTrim() (release func(), ok bool) {
	if !d.Enabled() {
		return nil, false
	}
	if !d.trimming.CompareAndSwap(false, true) {
		return nil, false
	}
	return func() { d.trimming.Store(false) }, true
}

// MaybeTrim 在后台触发一次清理，调用方不必等它结束。
// 节流两层：十分钟内不重复触发，且同一时刻只有一个清理协程。
func (d *Disk) MaybeTrim() {
	if !d.Enabled() {
		return
	}
	d.trimMu.Lock()
	due := time.Since(d.lastTrim) >= trimInterval
	unlimited := d.ttl <= 0 && d.maxBytes <= 0
	d.trimMu.Unlock()
	if unlimited {
		return
	}
	if !due {
		return
	}
	release, ok := d.BeginTrim()
	if !ok {
		return
	}
	go func() {
		defer release()
		if _, _, err := d.Trim(); err != nil {
			// 清理失败只影响容量，不影响正确性，不打日志以免刷屏。
			_ = err
		}
	}()
}

// recordTrim 记下本次清理时间，用于 MaybeTrim 的节流判断。
func (d *Disk) recordTrim() {
	d.trimMu.Lock()
	d.lastTrim = time.Now()
	d.trimMu.Unlock()
}
