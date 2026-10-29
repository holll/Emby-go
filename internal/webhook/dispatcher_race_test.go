package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestReproCloseRacesWithSend 等价 docs/2026-10-29-go-review.md §5.2 的最小复现：
// 多个 goroutine 循环 Subscribed+Send，同时 Close。修复前 Close 无锁把
// Dispatcher.targets 置 nil，与读侧的 slice header 遍历竞争，-race 必报，
// 且读侧可能读到撕裂的 ptr/len → 越界 panic。
func TestReproCloseRacesWithSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	d, err := NewDispatcher([]Target{{ID: "t1", Name: "t1", Enabled: true, URL: srv.URL,
		Events: []string{ItemRate}, TimeoutSeconds: 2, AllowPrivate: true}}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				d.Subscribed(ItemRate)
				d.Send(context.Background(), Event{Event: ItemRate})
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	d.Close()
	close(stop)
	wg.Wait()
}

// TestDispatcherCloseThenUseIsSafe 固定 Close 之后（以及重复 Close、nil receiver）
// 的可观察契约：Subscribed 返回 false，Send 返回 disabled 且不带任何目标结果。
func TestDispatcherCloseThenUseIsSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	d, err := NewDispatcher([]Target{testTarget("wh_a", srv.URL, ItemRate)}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Subscribed(ItemRate) {
		t.Fatal("expected subscription before Close")
	}
	d.Close()
	if d.Subscribed(ItemRate) {
		t.Fatal("Subscribed must be false after Close")
	}
	result := d.Send(context.Background(), Event{Event: ItemRate})
	if result.Status != ResultDisabled || len(result.Targets) != 0 {
		t.Fatalf("Send after Close = %+v, want disabled with no targets", result)
	}
	// 幂等：重复 Close 不得 panic，也不得再次关闭同一批 sender。
	d.Close()
	d.Close()

	var nilDispatcher *Dispatcher
	nilDispatcher.Close()
	if nilDispatcher.Subscribed(ItemRate) {
		t.Fatal("nil dispatcher must not report subscriptions")
	}
	if got := nilDispatcher.Send(context.Background(), Event{Event: ItemRate}); got.Status != ResultDisabled {
		t.Fatalf("nil dispatcher Send status = %q, want disabled", got.Status)
	}

	// 空目标集合（全部关闭）也是合法快照。
	empty, err := NewDispatcher(nil, DefaultEventBudget)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Subscribed(ItemRate) {
		t.Fatal("empty dispatcher must not report subscriptions")
	}
	if got := empty.Send(context.Background(), Event{Event: ItemRate}); got.Status != ResultDisabled {
		t.Fatalf("empty dispatcher Send status = %q, want disabled", got.Status)
	}
	empty.Close()
}
