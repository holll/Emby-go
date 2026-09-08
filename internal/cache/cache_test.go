package cache

import (
	"testing"
	"time"
)

func TestMemoryLRUAndTTL(t *testing.T) {
	c := NewMemory(1)
	c.Set("a", []byte("one"), time.Hour)
	if got, ok := c.Get("a"); !ok || string(got) != "one" {
		t.Fatal("memory cache miss")
	}
	c.Set("b", []byte("two"), time.Hour)
	if _, ok := c.Get("a"); ok {
		t.Fatal("LRU did not evict")
	}
	c.Set("c", []byte("three"), time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("c"); ok {
		t.Fatal("TTL did not expire")
	}
}

func TestRedisBehavior(t *testing.T) {
	c := NewRedis("127.0.0.1:6379", "", 15)
	if err := c.Ping(); err != nil {
		t.Skipf("local Redis unavailable: %v", err)
	}
	c.Clear()
	c.Set("test", []byte("value"), time.Minute)
	got, ok := c.Get("test")
	if !ok || string(got) != "value" {
		t.Fatal("redis cache read mismatch")
	}
	c.Delete("test")
	if _, ok := c.Get("test"); ok {
		t.Fatal("redis delete failed")
	}
	c.Set("clear-test", []byte("value"), time.Minute)
	c.Clear()
	if _, ok := c.Get("clear-test"); ok {
		t.Fatal("redis clear failed")
	}
}
