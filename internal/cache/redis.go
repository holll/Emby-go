package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "emby-go:cache:"

type Redis struct {
	client *redis.Client
	ctx    context.Context
}

func NewRedis(addr, password string, db int) *Redis {
	return &Redis{client: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db}), ctx: context.Background()}
}
func (r *Redis) key(key string) string { return keyPrefix + key }
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
		keys, next, err := r.client.Scan(r.ctx, cursor, keyPrefix+"*", 100).Result()
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
