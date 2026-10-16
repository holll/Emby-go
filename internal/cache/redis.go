package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client    *redis.Client
	ctx       context.Context
	keyPrefix string
}

// namespace 使用数据库持久化的随机身份，不能用容器内可能完全相同的数据库路径。
func NewRedis(addr, password string, db int, namespace string) *Redis {
	prefix := fmt.Sprintf("emby-go:cache:%x:", sha256.Sum256([]byte(namespace)))
	return &Redis{client: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db}), ctx: context.Background(), keyPrefix: prefix}
}
func (r *Redis) key(key string) string { return r.keyPrefix + key }
func (r *Redis) Get(key string) ([]byte, bool) {
	value, err := r.client.Get(r.ctx, r.key(key)).Bytes()
	return value, err == nil
}
func (r *Redis) Set(key string, value []byte, ttl time.Duration) {
	_ = r.client.Set(r.ctx, r.key(key), value, ttl).Err()
}
func (r *Redis) Delete(key string) { _ = r.client.Del(r.ctx, r.key(key)).Err() }
func (r *Redis) Clear() {
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(r.ctx, cursor, r.keyPrefix+"*", 100).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			_ = r.client.Del(r.ctx, keys...).Err()
		}
		if next == 0 {
			return
		}
		cursor = next
	}
}
func (r *Redis) Ping() error { return r.client.Ping(r.ctx).Err() }
