package cache

import (
	"sync"
	"testing"
	"time"
)

// View 返回的是缓存自己持有的切片：同 key 两次 View 共享底层数组，
// 且不分配（Get 每次都会 clone 一份）。
func TestViewSharesBackingArray(t *testing.T) {
	c := NewMemory(8)
	c.Set("k", []byte("payload"), time.Hour)

	a, ok := c.View("k")
	if !ok || string(a) != "payload" {
		t.Fatalf("View = %q, %v", a, ok)
	}
	b, ok := c.View("k")
	if !ok {
		t.Fatal("second View missed")
	}
	if &a[0] != &b[0] {
		t.Fatal("View copied: two calls must share the cached backing array")
	}
	if allocs := testing.AllocsPerRun(50, func() {
		if _, hit := c.View("k"); !hit {
			t.Fatal("miss")
		}
	}); allocs > 1 {
		t.Fatalf("View allocated %.1f times/op, want ≤1 (map lookup only)", allocs)
	}
}

// Set 会防御性复制入参：调用方改自己的切片不得影响已缓存的视图。
func TestViewImmutableAcrossSetMutation(t *testing.T) {
	c := NewMemory(8)
	value := []byte("original")
	c.Set("k", value, time.Hour)
	value[0] = 'X'

	got, ok := c.View("k")
	if !ok || string(got) != "original" {
		t.Fatalf("View = %q, want original (Set must copy)", got)
	}
}

// SetBytes 接管所有权：之后整体替换条目，不得原地改写已发布的值。
func TestViewSurvivesReplacement(t *testing.T) {
	c := NewMemory(8)
	c.SetBytes("k", []byte("first"), time.Hour)
	old, ok := c.View("k")
	if !ok {
		t.Fatal("miss")
	}
	c.SetBytes("k", []byte("second"), time.Hour)
	if string(old) != "first" {
		t.Fatalf("published view mutated in place: %q", old)
	}
	if now, _ := c.View("k"); string(now) != "second" {
		t.Fatalf("View after replacement = %q", now)
	}
}

func TestViewMissAndExpiry(t *testing.T) {
	c := NewMemory(8)
	if _, ok := c.View("missing"); ok {
		t.Fatal("View hit on missing key")
	}
	c.Set("short", []byte("x"), time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.View("short"); ok {
		t.Fatal("View served an expired entry")
	}
}

// View 与 Set/Delete 并发：视图要么是旧的完整值、要么是新的完整值，
// 不能出现长度与内容撕裂（-race 下还必须无数据竞争）。
func TestViewConcurrentWithSet(t *testing.T) {
	c := NewMemory(8)
	payloads := [][]byte{[]byte("aaaa"), []byte("bbbbbb"), []byte("cc")}
	c.SetBytes("k", payloads[0], time.Hour)

	var wg sync.WaitGroup
	stop := make(chan struct{})
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
				if v, ok := c.View("k"); ok {
					if len(v) != 4 && len(v) != 6 && len(v) != 2 {
						t.Errorf("torn view length %d", len(v))
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 2000; i++ {
		c.SetBytes("k", payloads[i%len(payloads)], time.Hour)
	}
	close(stop)
	wg.Wait()
}
