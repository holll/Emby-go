package store

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func newGateFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// P2-13 回归：等待写锁必须能被 ctx 取消。旧实现是 `for !TryLock() { 5ms timer }`
// 自旋——它只在 TryLock 失败之后才检查 ctx，所以「ctx 已取消但锁空闲」时仍会
// 拿到锁并返回 nil，等于让一个已取消的请求白白开一个写事务。writerGate 先检查
// ctx 再入队，取消后绝不返回 nil，也绝不留下令牌。
func TestWriterGateCancelledContextNeverAcquiresLock(t *testing.T) {
	s := newGateFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wrote := make(chan struct{}, 1)
	err := s.writeTxContext(ctx, func(*sql.Tx) error {
		wrote <- struct{}{}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeTxContext with cancelled ctx = %v, want context.Canceled", err)
	}
	select {
	case <-wrote:
		t.Fatal("write callback ran for an already-cancelled context")
	default:
	}
	// 取消的请求不能留下令牌：锁必须仍然空闲。
	if !s.writeMu.TryLock() {
		t.Fatal("writer gate still held after a cancelled wait (token leaked)")
	}
	s.writeMu.Unlock()
}

// P2-13 回归：取消要在毫秒级退出等待，而不是每 5ms 唤醒一次再检查。
func TestWriterGateContextCancelExitsQueuedWait(t *testing.T) {
	s := newGateFixture(t)
	s.writeMu.Lock()         // 模拟长时间持锁的写者（例如扫描提交）
	defer s.writeMu.Unlock() // 必须在等待者退出之后再释放

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- s.writeTxContext(ctx, func(*sql.Tx) error { return nil })
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // 让等待者真正入队

	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued wait after cancel = %v, want context.Canceled", err)
		}
		if d := time.Since(begin); d > 50*time.Millisecond {
			t.Fatalf("queued wait took %v to observe cancellation, want millisecond-level", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued waiter did not exit after ctx cancellation")
	}
}

// P2-13 回归：多写者按到达顺序获得写锁（FIFO），不会被后来者反复插队饿死。
// 旧实现靠 TryLock 自旋抢占，唤醒顺序由 5ms 定时器决定，与入队顺序无关。
func TestWriterGateServesWaitersInFIFOOrder(t *testing.T) {
	s := newGateFixture(t)
	const waiters = 8

	s.writeMu.Lock()
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	queued := make([]chan struct{}, waiters)
	for i := 0; i < waiters; i++ {
		queued[i] = make(chan struct{})
	}
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			close(queued[i]) // 已到达，即将入队
			if err := s.lockWriter(context.Background()); err != nil {
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			s.writeMu.Unlock()
		}(i)
		<-queued[i]
		// 逐个入队：上一个已经排在队列里，再放下一个，顺序才是确定的。
		time.Sleep(2 * time.Millisecond)
	}
	s.writeMu.Unlock() // 放行整条队列
	wg.Wait()

	mu.Lock()
	got := append([]int(nil), order...)
	mu.Unlock()
	if len(got) != waiters {
		t.Fatalf("acquired %d/%d times: %v", len(got), waiters, got)
	}
	for i, who := range got {
		if who != i {
			t.Fatalf("writer order = %v, want 0..%d (FIFO)", got, waiters-1)
		}
	}
}

// P2-13 回归：被取消的等待者不能泄漏 goroutine。
func TestWriterGateCancelledWaitersDoNotLeak(t *testing.T) {
	s := newGateFixture(t)
	baseline := runtime.NumGoroutine()

	s.writeMu.Lock()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			_ = s.lockWriter(ctx)
		}()
	}
	s.writeMu.Unlock()
	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines leaked: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}
