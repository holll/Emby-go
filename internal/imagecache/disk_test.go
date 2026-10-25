package imagecache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// listVariants 返回目录下所有变体文件路径（测试里用来数「生成了几份」）。
func listVariants(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), variantExt) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// newTestDisk 打开缓存并抑制后台清理：Put 会顺带触发 MaybeTrim，
// 而测试要观察的是 Trim 的确定性结果，不是后台协程的时序。
func newTestDisk(t *testing.T, dir string, maxBytes int64, ttl time.Duration, enabled bool) *Disk {
	t.Helper()
	disk, err := Open(dir, maxBytes, ttl, enabled)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	disk.recordTrim()
	return disk
}

func TestDiskPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	disk := newTestDisk(t, dir, 0, 0, true)
	if !disk.Enabled() {
		t.Fatal("应处于启用状态")
	}
	key := "v1|t:/media/poster.jpg:12345:50x25:q0"
	data := []byte("webp-bytes")
	if err := disk.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := disk.Get(key)
	if !ok {
		t.Fatal("Put 之后应能 Get 命中")
	}
	if string(got) != string(data) {
		t.Errorf("Get 字节不一致: %q", got)
	}
	// 变体落在磁盘上，且没有留下临时文件。
	if path := disk.PathFor(key); path == "" {
		t.Fatal("PathFor 应返回路径")
	} else if _, err := os.Stat(path); err != nil {
		t.Errorf("变体文件不存在: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(disk.PathFor(key)))
	if err != nil {
		t.Fatalf("读分片目录: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tmpPrefix) {
			t.Errorf("残留临时文件: %s", entry.Name())
		}
	}
}

func TestDiskKeyIsStableAndDistinct(t *testing.T) {
	disk := newTestDisk(t, t.TempDir(), 0, 0, true)
	base := "v1|t:/media/poster.jpg:1:50x25:q0"
	if disk.PathFor(base) != disk.PathFor(base) {
		t.Error("同一个 key 必须映射到同一个文件")
	}
	variants := []string{
		"v1|t:/media/poster.jpg:1:60x30:q0",  // 尺寸变了
		"v1|t:/media/poster.jpg:2:50x25:q0",  // 源图 mtime 变了
		"v1|t:/media/poster2.jpg:1:50x25:q0", // 文件变了
	}
	seen := map[string]string{base: base}
	for _, key := range variants {
		path := disk.PathFor(key)
		if other, ok := seen[path]; ok {
			t.Errorf("不同 key 撞到同一文件: %q 与 %q", key, other)
		}
		seen[path] = key
	}
}

func TestDiskTrimRemovesOldestAndExpired(t *testing.T) {
	dir := t.TempDir()
	// 上限 1500 字节，清理水位 80% = 1200：三个 1000 字节的变体必须淘汰到只剩一个。
	disk := newTestDisk(t, dir, 1500, 0, true)
	payload := make([]byte, 1000)
	keys := []string{"old", "middle", "new"}
	now := time.Now()
	for i, key := range keys {
		if err := disk.Put(key, payload); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
		// 伪造 mtime：old 最旧、new 最新（Trim 只按 mtime 判断"最近使用"）。
		stamp := now.Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(disk.PathFor(key), stamp, stamp); err != nil {
			t.Fatalf("Chtimes %s: %v", key, err)
		}
	}
	removed, freed, err := disk.Trim()
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if removed != 2 || freed != 2000 {
		t.Errorf("Trim 删除了 %d 个 / %d 字节，期望 2 / 2000", removed, freed)
	}
	if _, ok := disk.Get("new"); !ok {
		t.Error("最新的变体应保留")
	}
	if _, ok := disk.Get("old"); ok {
		t.Error("最旧的变体应被淘汰")
	}
	if got := len(listVariants(t, dir)); got != 1 {
		t.Errorf("目录里剩 %d 个变体，期望 1", got)
	}
}

func TestDiskTTLExpiresVariants(t *testing.T) {
	dir := t.TempDir()
	disk := newTestDisk(t, dir, 0, 24*time.Hour, true)
	if err := disk.Put("stale", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// 两天前的变体已经超过 24 小时保留期。
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(disk.PathFor("stale"), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if gone, _, err := disk.Trim(); err != nil || gone != 1 {
		t.Fatalf("Trim 应删除过期变体: removed=%d err=%v", gone, err)
	}
	if _, ok := disk.Get("stale"); ok {
		t.Error("过期变体不应再命中")
	}
}

func TestDiskDisabledIsNoop(t *testing.T) {
	for name, disk := range map[string]*Disk{
		"空目录":  func() *Disk { d, _ := Open("", 0, 0, true); return d }(),
		"显式关闭": func() *Disk { d, _ := Open(t.TempDir(), 0, 0, false); return d }(),
		"降级实例": Disabled(),
	} {
		if disk.Enabled() {
			t.Errorf("%s: 不应处于启用状态", name)
		}
		if err := disk.Put("k", []byte("v")); err != nil {
			t.Errorf("%s: 禁用时 Put 应静默成功: %v", name, err)
		}
		if _, ok := disk.Get("k"); ok {
			t.Errorf("%s: 禁用时 Get 不应命中", name)
		}
		if removed, _, err := disk.Trim(); removed != 0 || err != nil {
			t.Errorf("%s: 禁用时 Trim 应为空操作", name)
		}
	}
}

// 设置页要展示「当前占用」，并允许在线调整上限：统计不能算进临时文件，
// 上限调整必须立刻影响下一次 Trim。
func TestDiskStatsAndSetMaxBytes(t *testing.T) {
	dir := t.TempDir()
	disk := newTestDisk(t, dir, 1<<20, 0, true)
	if err := disk.Put("stats-a", []byte("aaaa")); err != nil {
		t.Fatal(err)
	}
	if err := disk.Put("stats-b", []byte("bbbbbb")); err != nil {
		t.Fatal(err)
	}
	// 残留的临时文件不该计入占用（它随时会被清理）。
	if err := os.WriteFile(filepath.Join(filepath.Dir(disk.PathFor("stats-a")), tmpPrefix+"leftover"), make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := disk.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 2 || st.Bytes != 10 {
		t.Fatalf("stats = %+v", st)
	}
	if got := disk.MaxBytes(); got != 1<<20 {
		t.Fatalf("MaxBytes = %d", got)
	}
	// 上限 6 字节 → 目标水位 4，只剩一份，释放 6 字节。
	disk.SetMaxBytes(6)
	removed, freed, err := disk.Trim()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || freed != 6 {
		t.Fatalf("removed=%d freed=%d", removed, freed)
	}
	if st, err = disk.Stats(); err != nil || st.Files != 1 || st.Bytes != 4 {
		t.Fatalf("after trim = %+v, %v", st, err)
	}
	// 0 表示不限制；负数同样归一成 0，而不是变成「小于零即永远超限」。
	disk.SetMaxBytes(0)
	if got := disk.MaxBytes(); got != 0 {
		t.Fatalf("MaxBytes after clear = %d", got)
	}
	disk.SetMaxBytes(-5)
	if got := disk.MaxBytes(); got != 0 {
		t.Fatalf("MaxBytes after negative = %d", got)
	}
	disabled := Disabled()
	if st, err = disabled.Stats(); err != nil || st.Files != 0 || st.Bytes != 0 {
		t.Fatalf("disabled stats = %+v, %v", st, err)
	}
	if disabled.MaxBytes() != 0 || disconnectedMax(disabled) != 0 {
		t.Fatal("disabled disk should report zero capacity")
	}
}

// disconnectedMax 覆盖 nil 接收者：App 字面量构造时 imgDisk 可能为 nil。
func disconnectedMax(d *Disk) int64 {
	var nilDisk *Disk
	_ = nilDisk
	return nilDisk.MaxBytes()
}
