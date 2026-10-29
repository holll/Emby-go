package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestIPBlockNetworkLiteralBoundaries(t *testing.T) {
	for input, want := range map[string]string{"198.51.100.8": "198.51.100.8/32", "198.51.100.8/24": "198.51.100.0/24", "2001:db8::123/64": "2001:db8::/64", "::ffff:198.51.100.8": "198.51.100.8/32", "::ffff:198.51.100.8/120": "198.51.100.0/24"} {
		got, err := NormalizeIPBlockNetwork(input)
		if err != nil || got.String() != want {
			t.Fatalf("%q = %v %v, want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"", "example.com", "https://198.51.100.8", "198.51.100.8:80", "127.2.3.4", "127.0.0.0/8", "0.0.0.0/0", "::/0", "::1", "0.0.0.0", "::", "::ffff:0:0/80", "fe80::1%eth0", "198.51.100.8/33", "10.0.0.0/8\nX"} {
		if _, err := NormalizeIPBlockNetwork(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestIPBlockRulesPersistenceCASAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	before := a.Version("g:version")
	rules, err := a.IPBlockRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Fatalf("initial %v %v", rules, err)
	}
	rules, err = a.MutateIPBlockRules(ctx, "198.51.100.7/24", "note", false)
	if err != nil || len(rules) != 1 || rules[0].Network != "198.51.100.0/24" {
		t.Fatalf("create %v %v", rules, err)
	}
	if _, err = b.MutateIPBlockRules(ctx, "198.51.100.8", "", false); !errors.Is(err, ErrIPBlockConflict) {
		t.Fatalf("overlap %v", err)
	}
	if _, err = b.MutateIPBlockRules(ctx, "198.51.100.0/24", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = b.MutateIPBlockRules(ctx, "198.51.100.0/24", "", true); !errors.Is(err, ErrIPBlockMissing) {
		t.Fatalf("missing %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 != 0 {
				s = b
			}
			_, e := s.MutateIPBlockRules(ctx, fmt.Sprintf("203.0.113.%d", i+1), "concurrent", false)
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	rules, err = b.IPBlockRules(ctx)
	if err != nil || len(rules) != 8 {
		t.Fatalf("lost updates %d %v", len(rules), err)
	}
	if a.Version("g:version") != before {
		t.Fatal("IP policy changed movie cache version")
	}
	for _, note := range []string{string(make([]byte, 201)), "bad\nvalue", "bad\u0085value"} {
		if _, err = a.MutateIPBlockRules(ctx, "192.0.2.30", note, false); err == nil {
			t.Fatal("accepted invalid note")
		}
	}
	// A cancelled caller cannot wait forever for a SQL pool connection.
	a.db.SetMaxOpenConns(1)
	conn, err := a.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	if _, err = a.IPBlockRules(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancel %v", err)
	}
	if _, err = a.MutateIPBlockRules(cancelled, "192.0.2.30", "", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("write cancel %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled policy operation blocked")
	}
}

func TestIPBlockRulesCorruptionAndLimitFailClosed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, raw := range []string{"", "null", "{}", `[{"network":"127.0.0.1/32","note":"","created_at":"2026-01-01T00:00:00Z"}]`, `[{"network":"198.51.100.8/24","note":"","created_at":"2026-01-01T00:00:00Z"}]`} {
		if _, err = s.db.Exec("INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", ipBlockKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err = s.IPBlockRules(ctx); err == nil {
			t.Fatalf("accepted corrupt %s", raw)
		}
	}
	if _, err = s.db.Exec("DELETE FROM kv WHERE key=?", ipBlockKey); err != nil {
		t.Fatal(err)
	}
	rules := make([]IPBlockRule, MaxIPBlockRules)
	for i := range rules {
		rules[i] = IPBlockRule{Network: fmt.Sprintf("10.0.%d.1/32", i), CreatedAt: "2026-01-01T00:00:00Z"}
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO kv(key,value) VALUES(?,?)", ipBlockKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MutateIPBlockRules(ctx, "192.0.2.1", "", false); !errors.Is(err, ErrIPBlockLimit) {
		t.Fatalf("limit %v", err)
	}
}
