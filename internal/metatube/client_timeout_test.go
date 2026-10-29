package metatube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestClientOptionsBindHeaderBudgetToTimeout 钉住「等响应头的预算 = 配置的单次请求超时」。
// 回归背景：metatube 的默认超时是 30s，而 outbound 的 ResponseHeaderTimeout 默认 15s。
// 不显式传递时，一次 20s 才回头的搜索会被 15s 提前掐断，表现为「刮削器不可用」。
func TestClientOptionsBindHeaderBudgetToTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
	}{
		{"default-30s", DefaultTimeout},
		{"custom-45s", 45 * time.Second},
		{"custom-90s", 90 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := newClientOptions(tc.timeout, nil)
			if opts.Timeout != tc.timeout {
				t.Fatalf("总超时 = %s, 期望 %s", opts.Timeout, tc.timeout)
			}
			if opts.ResponseHeaderTimeout != tc.timeout {
				t.Fatalf("等响应头预算 = %s, 期望跟随总超时 %s（outbound 默认 15s 会提前掐断）",
					opts.ResponseHeaderTimeout, tc.timeout)
			}
		})
	}
}

// TestSearchHonorsConfiguredHeaderBudget 用真实传输层验证预算确实生效：
// 服务端故意慢回响应头 300ms，客户端预算 2s 应成功、预算 100ms 应失败。
// 若等响应头仍固定 15s，第一个子例会成功但第二个也会「失败得很晚」，这里同时钉住耗时。
func TestSearchHonorsConfiguredHeaderBudget(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer slow.Close()

	t.Run("budget-covers-slow-header", func(t *testing.T) {
		client := New(slow.URL, "t", 2*time.Second)
		if _, err := client.SearchMovies(context.Background(), "ABF-018", "", true); err != nil {
			t.Fatalf("2s 预算下 300ms 慢响应头不应失败: %v", err)
		}
	})

	t.Run("budget-cuts-slow-header", func(t *testing.T) {
		client := New(slow.URL, "t", 100*time.Millisecond)
		start := time.Now()
		if _, err := client.SearchMovies(context.Background(), "ABF-018", "", true); err == nil {
			t.Fatal("100ms 预算下 300ms 慢响应头应失败")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("超时预算未生效：耗时 %s，远大于配置的 100ms", elapsed)
		}
	})
}
