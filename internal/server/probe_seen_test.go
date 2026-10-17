package server

import (
	"fmt"
	"testing"
	"time"
)

func probeObserveApp(clock func() time.Time) *App {
	return &App{
		probeSeen:     make(map[string]probeSeenEntry),
		probeNow:      clock,
		probeTokens:   probeWriteBurst,
		probeRefillAt: clock(),
	}
}

func TestObserveProbeDedupesWithinTTLAndExpires(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := probeObserveApp(func() time.Time { return now })
	const key = "GET /system/unmatched"

	if write, _ := a.observeProbe(key, now); !write {
		t.Fatal("首个未实现端点应落库")
	}
	if write, _ := a.observeProbe(key, now); write {
		t.Fatal("TTL 内重复端点不应重复落库")
	}
	if len(a.probeSeen) != 1 {
		t.Fatalf("seen=%d want 1", len(a.probeSeen))
	}

	now = now.Add(probeSeenTTL - time.Minute)
	if write, _ := a.observeProbe(key, now); write {
		t.Fatal("TTL 内最后 1 分钟仍应去重")
	}
	now = now.Add(2 * time.Minute)
	if write, _ := a.observeProbe(key, now); !write {
		t.Fatal("超过 TTL 后应重新采样")
	}
	if len(a.probeSeen) != 1 {
		t.Fatalf("重新采样后 seen=%d want 1", len(a.probeSeen))
	}
}

func TestObserveProbeEvictsOldestWhenFull(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := probeObserveApp(func() time.Time { return now })
	for i := 0; i < probeSeenLimit; i++ {
		a.probeSeen[fmt.Sprintf("GET /old-%d", i)] = probeSeenEntry{seenAt: now.Add(-time.Duration(i+1) * time.Minute)}
	}
	const key = "GET /system/new"
	if write, _ := a.observeProbe(key, now); !write {
		t.Fatal("容量满时新端点应淘汰最旧条目后落库")
	}
	if len(a.probeSeen) != probeSeenLimit {
		t.Fatalf("seen=%d want %d", len(a.probeSeen), probeSeenLimit)
	}
	if _, ok := a.probeSeen[key]; !ok {
		t.Fatal("新端点未进入 seen")
	}
	if _, ok := a.probeSeen["GET /old-127"]; ok {
		t.Fatal("最旧条目未被淘汰")
	}
	if _, ok := a.probeSeen["GET /old-0"]; !ok {
		t.Fatal("较新条目不应被淘汰")
	}
}

func TestObserveProbeWithoutBudgetDoesNotMarkSeen(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := probeObserveApp(func() time.Time { return now })
	a.probeTokens = 0
	const key = "POST /unmatched"
	if write, _ := a.observeProbe(key, now); write {
		t.Fatal("预算耗尽时不应落库")
	}
	if len(a.probeSeen) != 0 {
		t.Fatal("预算耗尽时不应占位，否则端点会被永久标记已见")
	}
	now = now.Add(500 * time.Millisecond)
	if write, _ := a.observeProbe(key, now); write {
		t.Fatal("半个令牌不足以落库")
	}
	now = now.Add(500 * time.Millisecond)
	if write, _ := a.observeProbe(key, now); !write {
		t.Fatal("令牌恢复后应允许落库")
	}
	if len(a.probeSeen) != 1 {
		t.Fatalf("seen=%d want 1", len(a.probeSeen))
	}
}

func TestObserveProbeTokenBurstIsCapped(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := probeObserveApp(func() time.Time { return now })
	a.probeTokens = 0
	a.probeRefillAt = now.Add(-time.Hour)
	if write, _ := a.observeProbe("GET /system/burst", now); !write {
		t.Fatal("突发预算应允许一次落库")
	}
	if a.probeTokens != probeWriteBurst-1 {
		t.Fatalf("tokens=%v want %v（闲 1 小时后上限仍为 burst）", a.probeTokens, probeWriteBurst-1)
	}
}

func TestObserveProbeDropsExpiredEntriesWhileEvicting(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := probeObserveApp(func() time.Time { return now })
	for i := 0; i < probeSeenLimit; i++ {
		a.probeSeen[fmt.Sprintf("GET /stale-%d", i)] = probeSeenEntry{seenAt: now.Add(-probeSeenTTL - time.Minute)}
	}
	if write, _ := a.observeProbe("GET /system/fresh", now); !write {
		t.Fatal("过期条目清理后新端点应落库")
	}
	if len(a.probeSeen) != 1 {
		t.Fatalf("seen=%d want 1（过期项应全部清掉）", len(a.probeSeen))
	}
}
