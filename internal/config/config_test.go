package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
