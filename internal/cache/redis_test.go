package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 在内存中截获 Redis 命令，隔离测试无需外部 Redis 进程。
type redisIsolationHook struct{ values map[string]string }

func (h *redisIsolationHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *redisIsolationHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *redisIsolationHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		switch cmd.Name() {
		case "set":
			h.values[fmt.Sprint(args[1])] = string(args[2].([]byte))
			cmd.(*redis.StatusCmd).SetVal("OK")
		case "get":
			value, ok := h.values[fmt.Sprint(args[1])]
			if !ok {
				cmd.SetErr(redis.Nil)
				return redis.Nil
			}
			cmd.(*redis.StringCmd).SetVal(value)
		case "del":
			var n int64
			for _, key := range args[1:] {
				k := fmt.Sprint(key)
				if _, ok := h.values[k]; ok {
					delete(h.values, k)
					n++
				}
			}
			cmd.(*redis.IntCmd).SetVal(n)
		case "scan":
			prefix := strings.TrimSuffix(fmt.Sprint(args[3]), "*")
			var keys []string
			for key := range h.values {
				if strings.HasPrefix(key, prefix) {
					keys = append(keys, key)
				}
			}
			cmd.(*redis.ScanCmd).SetVal(keys, 0)
		default:
			return fmt.Errorf("unexpected Redis command: %s", cmd.Name())
		}
		return nil
	}
}

func TestRedisInstanceNamespace(t *testing.T) {
	a := NewRedis("unused:6379", "secret-a", 0, "database-identity-a")
	same := NewRedis("other:6379", "secret-b", 0, "database-identity-a")
	other := NewRedis("unused:6379", "secret-a", 0, "database-identity-b")
	defer a.client.Close()
	defer same.client.Close()
	defer other.client.Close()
	want := fmt.Sprintf("emby-go:cache:%x:auth:token", sha256.Sum256([]byte("database-identity-a")))
	if a.key("auth:token") != want {
		t.Fatalf("namespace key=%q want=%q", a.key("auth:token"), want)
	}
	if a.key("auth:token") != same.key("auth:token") {
		t.Fatal("equivalent DB paths/password changes changed namespace")
	}
	if a.key("auth:token") == other.key("auth:token") {
		t.Fatal("different SQLite instances share keys")
	}
	hook := &redisIsolationHook{values: map[string]string{"emby-go:cache:auth:legacy": "accepted", "unrelated": "keep"}}
	for _, r := range []*Redis{a, same, other} {
		r.client.AddHook(hook)
	}
	if _, ok := a.Get("auth:legacy"); ok {
		t.Fatal("legacy unisolated token accepted")
	}
	a.Set("auth:token", []byte("a"), time.Minute)
	if _, ok := other.Get("auth:token"); ok {
		t.Fatal("cross-instance auth token accepted")
	}
	if value, ok := same.Get("auth:token"); !ok || string(value) != "a" {
		t.Fatal("stable namespace cache miss")
	}
	other.Set("auth:token", []byte("b"), time.Minute)
	a.Clear()
	if _, ok := a.Get("auth:token"); ok {
		t.Fatal("Clear left own keys")
	}
	if value, ok := other.Get("auth:token"); !ok || string(value) != "b" {
		t.Fatal("Clear deleted other instance")
	}
	if hook.values["emby-go:cache:auth:legacy"] != "accepted" || hook.values["unrelated"] != "keep" {
		t.Fatal("Clear deleted legacy or unrelated keys")
	}
}
