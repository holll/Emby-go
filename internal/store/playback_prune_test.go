package store

import (
	"database/sql"
	"sync"
	"testing"
	"time"
)

func playbackEventStats(t *testing.T, s *Store) (count int64, maxID int64) {
	t.Helper()
	if err := s.db.QueryRow("SELECT COUNT(*), COALESCE(MAX(id),0) FROM playback_events").Scan(&count, &maxID); err != nil {
		t.Fatal(err)
	}
	return count, maxID
}

// bulkInsertPlaybackEvents 在单个写事务里灌入 n 条事件行，用来把表撑到上界附近；
// 逐条走 RecordPlaybackEvent 需要 2 万次提交，测试太慢。
func bulkInsertPlaybackEvents(t *testing.T, s *Store, sessionKey string, n int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.writeTx(func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			if _, err := tx.Exec(`INSERT INTO playback_events(user_id,session_key,seq,kind,position_ticks,created_at) VALUES(1,?,?,'progress',0,?)`,
				sessionKey, 1_000_000+i, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("bulk insert %d playback events: %v", n, err)
	}
}

// P2-12 回归：prune 按事件窗口触发——连续 playbackPruneEvery 条事件里只应有 1 次
// prune（旧实现是每条事件都在写事务里跑一次 NOT IN 子查询，即每条一次）。
func TestPlaybackPruneRunsOncePerWindow(t *testing.T) {
	s, id := newPlaybackFixture(t)

	var mu sync.Mutex
	prunes := 0
	playbackPruneHook = func() {
		mu.Lock()
		prunes++
		mu.Unlock()
	}
	t.Cleanup(func() { playbackPruneHook = nil })

	record(t, s, id, "window", PlaybackStart, 0)
	_, cur := playbackEventStats(t, s)
	// 先补齐到窗口边界（这一段本身可能命中一次 prune，不计入断言）。
	for cur%playbackPruneEvery != 0 {
		record(t, s, id, "window", PlaybackProgress, 0)
		cur++
	}
	mu.Lock()
	start := prunes
	mu.Unlock()

	for i := 0; i < playbackPruneEvery; i++ {
		record(t, s, id, "window", PlaybackProgress, int64(i))
	}

	mu.Lock()
	got := prunes - start
	mu.Unlock()
	if got != 1 {
		t.Fatalf("prune ran %d times across one window of %d events, want 1", got, playbackPruneEvery)
	}
}

// P2-12 回归：窗口内不做 prune（不是每条事件都做），但表行数上界语义必须保住。
func TestPlaybackPruneKeepsEventTableBounded(t *testing.T) {
	s, id := newPlaybackFixture(t)

	var mu sync.Mutex
	prunes := 0
	playbackPruneHook = func() {
		mu.Lock()
		prunes++
		mu.Unlock()
	}
	t.Cleanup(func() { playbackPruneHook = nil })

	record(t, s, id, "bound", PlaybackStart, 0)
	// 灌到上界以上：先撑满 retain 行，再多加 50 行留出窗口余量。
	bulkInsertPlaybackEvents(t, s, "bound", playbackEventsRetain+50)
	count, _ := playbackEventStats(t, s)
	if count <= playbackEventsRetain {
		t.Fatalf("fixture did not exceed the retain bound: %d rows", count)
	}

	// 不命中窗口的事件不得触发 prune，表也只能在上界 + 窗口内增长（不会被无限撑大）。
	_, cur := playbackEventStats(t, s)
	if (cur+1)%playbackPruneEvery == 0 {
		record(t, s, id, "bound", PlaybackProgress, 0) // 对齐掉即将命中的窗口
	}
	mu.Lock()
	before := prunes
	mu.Unlock()
	record(t, s, id, "bound", PlaybackProgress, 1)
	mu.Lock()
	after := prunes
	mu.Unlock()
	if after != before {
		t.Fatalf("prune ran outside the window (%d -> %d)", before, after)
	}
	count, _ = playbackEventStats(t, s)
	if count > playbackEventsRetain+playbackPruneEvery {
		t.Fatalf("event table grew past the bound: %d rows, want <= %d", count, playbackEventsRetain+playbackPruneEvery)
	}

	// 命中窗口的事件把表裁回上界。
	_, cur = playbackEventStats(t, s)
	for (cur+1)%playbackPruneEvery != 0 {
		record(t, s, id, "bound", PlaybackProgress, 2)
		cur++
	}
	record(t, s, id, "bound", PlaybackProgress, 3)
	mu.Lock()
	pruneCount := prunes
	mu.Unlock()
	if pruneCount <= before {
		t.Fatal("window-aligned event did not trigger a prune")
	}
	count, _ = playbackEventStats(t, s)
	if count > playbackEventsRetain {
		t.Fatalf("after prune event table holds %d rows, want <= %d", count, playbackEventsRetain)
	}
}
