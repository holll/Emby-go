package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func newAPIKeyBackfillFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/apikeys.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// insertLegacyAPIKeyRow 直接插一行没有 stable_id 的旧格式记录，模拟「升级后由
// 外部工具/旧代码写入」的行——这正是 P2-11 里回填逻辑唯一会命中的场景。
func insertLegacyAPIKeyRow(t *testing.T, s *Store, plain string) {
	t.Helper()
	if _, err := s.db.Exec(
		"INSERT INTO api_keys(key,key_hash,key_prefix,name,created_at,user_id) VALUES(?,?,?,?,?,1)",
		plain, apiKeyHash(plain), plain[:8], "legacy", time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert legacy api key row: %v", err)
	}
}

// P2-11 回归：旧格式行的 stable_id 回填必须走 s.writeTx，而不是裸 s.db.Exec。
// 断言方式：占住进程内写锁（模拟写者长时间持锁），回填在此期间不得完成；释放后
// 才完成且返回落库的稳定 ID。旧实现在持锁期间照旧立刻完成，这条断言会失败。
func TestResolveAPIKeyStableIDQueuesBehindWriterGate(t *testing.T) {
	s := newAPIKeyBackfillFixture(t)
	const plain = "legacy-plain-api-key-0001"
	insertLegacyAPIKeyRow(t, s, plain)

	type result struct {
		stable string
		err    error
	}
	done := make(chan result, 1)

	s.writeMu.Lock()
	go func() {
		stable, err := s.ResolveAPIKeyStableID(plain)
		done <- result{stable, err}
	}()

	select {
	case got := <-done:
		s.writeMu.Unlock()
		t.Fatalf("backfill completed while the writer gate was held (%+v): it bypassed writeTx", got)
	case <-time.After(300 * time.Millisecond):
	}
	s.writeMu.Unlock()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("resolve after release: %v (must not surface a locked database)", got.err)
		}
		if got.stable == "" {
			t.Fatal("resolve returned an empty stable id")
		}
		var stored string
		if err := s.db.QueryRow("SELECT COALESCE(stable_id,'') FROM api_keys WHERE key=?", plain).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != got.stable {
			t.Fatalf("returned stable id %q but row holds %q", got.stable, stored)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolve did not complete after the writer gate was released")
	}
}

// P2-11 回归：鉴权/轮转路径上的写不能继承 busy_timeout（10s）无限期等待。写者
// 持锁超过预算时回填必须按预算放弃，并给出可重试的错误，而不是把请求挂死或返回
// "database is locked"。
func TestResolveAPIKeyStableIDBackfillWaitIsBounded(t *testing.T) {
	s := newAPIKeyBackfillFixture(t)
	const plain = "legacy-plain-api-key-0002"
	insertLegacyAPIKeyRow(t, s, plain)

	s.writeMu.Lock()
	begin := time.Now()
	stable, err := s.ResolveAPIKeyStableID(plain)
	elapsed := time.Since(begin)
	s.writeMu.Unlock()

	if err == nil {
		t.Fatalf("backfill waited out the writer gate and succeeded (%q), want a bounded failure", stable)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("backfill error = %v, want context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("backfill surfaced a locked database to the auth path: %v", err)
	}
	if elapsed > apiKeyBackfillWaitBudget+2*time.Second {
		t.Fatalf("backfill waited %v, want about %v", elapsed, apiKeyBackfillWaitBudget)
	}
	// 放弃等待后锁不能被带走，后续写者必须能立刻拿到。
	if !s.writeMu.TryLock() {
		t.Fatal("writer gate leaked after a timed-out backfill")
	}
	s.writeMu.Unlock()
}

// P2-11 回归：并发解析同一旧行必须收敛到同一个稳定 ID（重试/并发不能互相覆盖）。
func TestResolveAPIKeyStableIDConcurrentBackfillConverges(t *testing.T) {
	s := newAPIKeyBackfillFixture(t)
	const plain = "legacy-plain-api-key-0003"
	insertLegacyAPIKeyRow(t, s, plain)

	const workers = 8
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.ResolveAPIKeyStableID(plain)
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("workers disagree: %v", results)
		}
	}
	var stored string
	if err := s.db.QueryRow("SELECT COALESCE(stable_id,'') FROM api_keys WHERE key=?", plain).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != results[0] {
		t.Fatalf("row holds %q, resolvers reported %q", stored, results[0])
	}
}
