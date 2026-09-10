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
	var cfg Config
	if got := cfg.ProbeTimeout(); got != 90*time.Second {
		t.Errorf("默认探测超时 = %v, want 90s", got)
	}
	if got := cfg.ProbeWorkers(); got != 2 {
		t.Errorf("默认并发 = %d, want 2", got)
	}
	cfg.ProbeTimeoutSec, cfg.ProbeConcurrency = 30, 4
	if got := cfg.ProbeTimeout(); got != 30*time.Second {
		t.Errorf("探测超时 = %v, want 30s", got)
	}
	if got := cfg.ProbeWorkers(); got != 4 {
		t.Errorf("并发 = %d, want 4", got)
	}
	// 并发上限 8：过高对远程源没有收益。
	cfg.ProbeConcurrency = 64
	if got := cfg.ProbeWorkers(); got != 8 {
		t.Errorf("并发上限 = %d, want 8", got)
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
	if cfg.ProbeTimeout() != 45*time.Second || cfg.ProbeWorkers() != 3 {
		t.Errorf("探测配置 = %v/%d", cfg.ProbeTimeout(), cfg.ProbeWorkers())
	}
}
