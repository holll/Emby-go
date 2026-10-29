package scraper

import (
	"fmt"
	"testing"
	"time"
)

// imageCommitEntryCount 读出当前驻留的提交锁条目数（仅测试用）。
func imageCommitEntryCount() int {
	imageCommitGuard.Lock()
	defer imageCommitGuard.Unlock()
	return len(imageCommitLocks)
}

// TestLockImageCommitReleasesEntries 验证图片提交锁条目随释放回收。
//
// 旧实现是只增不删的 sync.Map：条目随「见过的图片目标路径数」永久驻留
// （每条约 100B+），一部片 N 张图 × 全库影片，长跑进程的内存只涨不降。
func TestLockImageCommitReleasesEntries(t *testing.T) {
	if got := imageCommitEntryCount(); got != 0 {
		t.Fatalf("用例开始前已有 %d 个条目，测试前提不成立", got)
	}
	for i := range 200 {
		key := fmt.Sprintf("/library/movie-%03d/poster.webp", i)
		release := lockImageCommit(key)
		if got := imageCommitEntryCount(); got != 1 {
			release()
			t.Fatalf("持有 %s 期间条目数 = %d，期望 1", key, got)
		}
		release()
	}
	if got := imageCommitEntryCount(); got != 0 {
		t.Fatalf("释放后仍驻留 %d 个条目：只增不删，内存随历史图片路径数增长", got)
	}
}

// TestLockImageCommitSerializesSameTarget 验证同一目标路径互斥、不同路径不互斥。
//
// 互斥是必须的（两个刮削请求同时 rename 到同一张 poster.webp 会互相覆盖/失败，
// Windows 上还是共享冲突）；但如果为了回收条目而误伤互斥（例如淘汰掉仍被持有的锁），
// 这里会看到同一路径被两路同时持有。
func TestLockImageCommitSerializesSameTarget(t *testing.T) {
	const key = "/library/movie-001/poster.webp"
	release := lockImageCommit(key)

	// 不同路径不该被这把锁挡住。
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		other := lockImageCommit("/library/movie-002/poster.webp")
		other()
	}()
	select {
	case <-otherDone:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("不同目标路径被串行化了：锁粒度不是「每个路径一把」")
	}

	// 同一路径必须排队。
	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		same := lockImageCommit(key)
		same()
	}()
	select {
	case <-acquired:
		release()
		<-acquired
		t.Fatal("同一目标路径被两路同时持锁：图片提交互斥失效")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("释放后等待者仍拿不到锁")
	}
	if got := imageCommitEntryCount(); got != 0 {
		t.Fatalf("全部释放后仍有 %d 个条目驻留", got)
	}
}
