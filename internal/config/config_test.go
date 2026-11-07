package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistServerIDReplaceInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := "# 注释保留\nlisten: \":18080\"\ndb_path: \"emby-go.db\"\nserver_name: \"My\"\nserver_id: \"\"\nredis_addr: \"127.0.0.1:6379\"\n"
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PersistServerID("abc123"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, "# 注释保留") {
		t.Fatalf("注释丢失:\n%s", s)
	}
	if !strings.Contains(s, `server_id: "abc123"`) {
		t.Fatalf("server_id 未回写:\n%s", s)
	}
	if strings.Contains(s, `server_id: ""`) {
		t.Fatalf("空 server_id 未被替换:\n%s", s)
	}
}

func TestPersistServerIDInsertMissingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := "listen: \":18080\"\nserver_name: \"My\"\nredis_addr: \"127.0.0.1:6379\"\n"
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PersistServerID("xyz789"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, `server_id: "xyz789"`) {
		t.Fatalf("缺少 server_id 时应插入:\n%s", s)
	}
}

func TestProbeDefaults(t *testing.T) {
	for _, tc := range []struct{ input, want int }{
		{0, 60}, {-1, 60}, {1, 1}, {30, 30}, {60, 60}, {61, 60}, {90, 60}, {int(^uint(0) >> 1), 60},
	} {
		if got := (Config{ProbeTimeoutSec: tc.input}).ProbeTimeout(); got != time.Duration(tc.want)*time.Second {
			t.Errorf("探测预算(%d) = %v, want %ds", tc.input, got, tc.want)
		}
	}
	for _, tc := range []struct{ input, want int }{
		{0, 2}, {-1, 2}, {1, 1}, {2, 2}, {3, 2}, {8, 2}, {64, 2},
	} {
		if got := (Config{ProbeConcurrency: tc.input}).ProbeWorkers(); got != tc.want {
			t.Errorf("探测 worker(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestProbeConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := "redis_addr: \"127.0.0.1:6379\"\nffprobe_path: \"/usr/bin/ffprobe\"\nprobe_timeout_seconds: 45\nprobe_concurrency: 3\n"
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FFProbePath != "/usr/bin/ffprobe" {
		t.Errorf("ffprobe_path = %q", cfg.FFProbePath)
	}
	if cfg.ProbeTimeout() != 45*time.Second || cfg.ProbeWorkers() != 2 {
		t.Errorf("探测配置 = %v/%d", cfg.ProbeTimeout(), cfg.ProbeWorkers())
	}
}

func TestLegacyProbeConfigEffectiveLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const src = "probe_timeout_seconds: 90\nprobe_concurrency: 8\n"
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProbeTimeoutSec != 90 || cfg.ProbeConcurrency != 8 || cfg.ProbeTimeout() != time.Minute || cfg.ProbeWorkers() != 2 {
		t.Fatalf("legacy probe configuration: raw=%d/%d effective=%v/%d", cfg.ProbeTimeoutSec, cfg.ProbeConcurrency, cfg.ProbeTimeout(), cfg.ProbeWorkers())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != src {
		t.Fatalf("load changed configuration: %q %v", got, err)
	}
}

func TestImageCacheDefaults(t *testing.T) {
	var cfg Config
	cfg.ImageCache = cfg.ImageCache.ApplyDefaults()
	if !cfg.ImageCache.IsEnabled() {
		t.Error("缺省应启用缩略图磁盘缓存")
	}
	if cfg.ImageCache.MaxBytes != defaultImageCacheMaxBytes {
		t.Errorf("默认容量 = %d, want %d", cfg.ImageCache.MaxBytes, defaultImageCacheMaxBytes)
	}
	if got := cfg.ImageCache.TTL(); got != defaultImageCacheTTLHours*time.Hour {
		t.Errorf("默认保留 = %v", got)
	}
	// 显式关闭不能被 ApplyDefaults 打开。
	disabled := false
	cfg.ImageCache.Enabled = &disabled
	cfg.ImageCache = cfg.ImageCache.ApplyDefaults()
	if cfg.ImageCache.IsEnabled() {
		t.Error("enabled: false 必须保持关闭")
	}
	// -1 表示不限制/不过期。
	cfg.ImageCache.MaxBytes, cfg.ImageCache.TTLHours = -1, -1
	if got := cfg.ImageCache.TTL(); got != 0 {
		t.Errorf("ttl_hours: -1 应表示永不过期，实际 %v", got)
	}
	if err := cfg.ImageCache.Validate(); err != nil {
		t.Errorf("-1 是合法值: %v", err)
	}
	cfg.ImageCache.MaxBytes = -2
	if err := cfg.ImageCache.Validate(); err == nil {
		t.Error("max_bytes: -2 应被拒绝")
	}
}

func TestImageCacheConfigFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	src := "image_cache:\n  enabled: false\n  dir: /var/lib/emby-go/thumbs\n  max_bytes: 1048576\n  ttl_hours: 24\n"
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ImageCache.IsEnabled() {
		t.Error("yaml 里的 enabled: false 应生效")
	}
	if cfg.ImageCache.Dir != "/var/lib/emby-go/thumbs" {
		t.Errorf("dir = %q", cfg.ImageCache.Dir)
	}
	if cfg.ImageCache.MaxBytes != 1048576 || cfg.ImageCache.TTL() != 24*time.Hour {
		t.Errorf("容量/保留 = %d/%v", cfg.ImageCache.MaxBytes, cfg.ImageCache.TTL())
	}
}
