package server

import (
	"testing"
	"time"

	"emby-go/internal/cache"
)

// spyCache 记录 Get/View 的调用次数，用来钉住"服务端读缓存优先走零拷贝视图"。
type spyCache struct {
	viewCalls int
	getCalls  int
	value     []byte
}

func (s *spyCache) Get(string) ([]byte, bool) {
	s.getCalls++
	return append([]byte(nil), s.value...), true
}
func (s *spyCache) View(string) ([]byte, bool)        { s.viewCalls++; return s.value, true }
func (s *spyCache) Set(string, []byte, time.Duration) {}
func (s *spyCache) Delete(string)                     {}
func (s *spyCache) Clear()                            {}

// getOnlyCache 只有 Cache 能力（等价 Redis 后端），必须退回 Get。
type getOnlyCache struct{ calls int }

func (g *getOnlyCache) Get(string) ([]byte, bool)         { g.calls++; return []byte("x"), true }
func (g *getOnlyCache) Set(string, []byte, time.Duration) {}
func (g *getOnlyCache) Delete(string)                     {}
func (g *getOnlyCache) Clear()                            {}

func TestCachedBodyPrefersZeroCopyView(t *testing.T) {
	spy := &spyCache{value: []byte(`{"Items":[]}`)}
	a := &App{cache: spy}

	got, ok := a.cachedBody("items:k")
	if !ok || string(got) != `{"Items":[]}` {
		t.Fatalf("cachedBody = %q, %v", got, ok)
	}
	if spy.viewCalls != 1 || spy.getCalls != 0 {
		t.Fatalf("want exactly one View and no Get, got view=%d get=%d", spy.viewCalls, spy.getCalls)
	}
}

func TestCachedBodyFallsBackToGet(t *testing.T) {
	only := &getOnlyCache{}
	a := &App{cache: only}

	got, ok := a.cachedBody("items:k")
	if !ok || string(got) != "x" {
		t.Fatalf("cachedBody = %q, %v", got, ok)
	}
	if only.calls != 1 {
		t.Fatalf("want one Get fallback, got %d", only.calls)
	}
}

// Memory 必须满足 cache.Viewer，否则服务端会静默退回复制路径。
func TestMemoryImplementsViewer(t *testing.T) {
	if _, ok := any(cache.NewMemory(8)).(cache.Viewer); !ok {
		t.Fatal("cache.Memory no longer implements cache.Viewer: server read path would silently copy")
	}
}
