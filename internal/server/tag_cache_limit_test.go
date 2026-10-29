package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// §2.3 #2：tags 表只在 invalidateImageTag 时删除条目，而 key 来自库内影片行。
// 库被删除/影片被移除后旧路径会永久驻留 —— 这里验证容量兜底真的生效，
// 且淘汰不会改变 tag 语义（文件没变 → 重新 stat 得到同一个 tag）。
func TestTagCacheIsBounded(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	a.tags = make(map[string]tagEntry)

	poster := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(poster, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := a.posterTag(poster)
	if want == "0" || want == "" {
		t.Fatalf("预热标签异常: %q", want)
	}

	// 灌到上限之上：8000 条"已过期"的陈旧路径 + 上面那张真实海报。
	old := time.Now().Add(-time.Hour)
	for i := 0; i < tagCacheLimit+tagCacheSlack+2000; i++ {
		a.tags[filepath.Join(dir, "gone", "p"+string(rune('a'+i%26))+itoa(i)+".jpg")] = tagEntry{tag: "x", ts: old}
	}
	// 上限是在"插入新条目"时兜底的（命中路径不扫描整表 —— 否则每次命中都要
	// O(n)，等于把热路径做慢）。这里以一次 miss 触发兜底。
	trigger := filepath.Join(dir, "trigger.jpg")
	if err := os.WriteFile(trigger, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = a.posterTag(trigger)

	if got := a.posterTag(poster); got != want {
		t.Fatalf("淘汰后 tag 变了: got %q want %q", got, want)
	}

	a.tagMu.Lock()
	n := len(a.tags)
	a.tagMu.Unlock()
	if n > tagCacheLimit {
		t.Fatalf("tag 缓存未收敛到上限: %d > %d", n, tagCacheLimit)
	}

	// 仍在 TTL 内的条目不该被误删：命中路径必须继续返回同一 tag 且不发 stat。
	fresh := filepath.Join(dir, "fresh.jpg")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := a.posterTag(fresh)
	if err := os.Remove(fresh); err != nil {
		t.Fatal(err)
	}
	if got := a.posterTag(fresh); got != tag {
		t.Fatalf("TTL 内的条目被误删：删文件后 tag 变成 %q（原为 %q）", got, tag)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// 清理必须是摊销的：若每次插入都整表扫 8192 条，饱和后一页 200 条（最多 ~600
// 次插入）就是几十万次 map 迭代。这里钉住"未超过 上限+余量 时一次都不扫"。
func TestTagPruneIsAmortized(t *testing.T) {
	dir := t.TempDir()
	a := &App{}
	a.tags = make(map[string]tagEntry)

	stale := time.Now().Add(-time.Hour)
	for i := 0; i < tagCacheLimit+tagCacheSlack-1; i++ {
		a.tags[filepath.Join(dir, "old", "p"+itoa(i)+".jpg")] = tagEntry{tag: "x", ts: stale}
	}
	before := len(a.tags)

	// 一次 miss：未达触发线，插进去但不清理（旧的过期条目仍在）。
	fresh := filepath.Join(dir, "amortized.jpg")
	if err := os.WriteFile(fresh, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = a.posterTag(fresh)

	a.tagMu.Lock()
	after := len(a.tags)
	a.tagMu.Unlock()
	if after != before+1 {
		t.Fatalf("未达触发线却触发了清理：%d → %d（预期 %d）", before, after, before+1)
	}

	// 再插一次越过触发线：这一次才清理，并压回上限。
	trigger := filepath.Join(dir, "trigger.jpg")
	if err := os.WriteFile(trigger, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = a.posterTag(trigger)

	a.tagMu.Lock()
	final := len(a.tags)
	a.tagMu.Unlock()
	if final > tagCacheLimit {
		t.Fatalf("清理后未压回上限：%d > %d", final, tagCacheLimit)
	}
}
