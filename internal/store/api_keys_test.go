package store

import (
	"path/filepath"
	"testing"
)

func TestAPIKeyHashingAndListing(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	created, err := s.CreateAPIKey("script")
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Key) != 48 {
		t.Fatalf("plaintext key length=%d", len(created.Key))
	}
	if len(created.ID) != 64 || created.KeyPrefix != created.Key[:12] {
		t.Fatalf("created id/prefix=%q/%q", created.ID, created.KeyPrefix)
	}
	if created.ID != apiKeyHash(created.Key) {
		t.Fatalf("id is not the digest")
	}
	items, err := s.APIKeys()
	if err != nil || len(items) != 1 {
		t.Fatalf("list=%v err=%v", items, err)
	}
	if items[0].Key != "" || items[0].ID != created.ID || items[0].KeyPrefix != created.KeyPrefix {
		t.Fatalf("list leaked or lost fields: %+v", items[0])
	}
	if ok, err := s.HasAPIKey(created.Key); err != nil || !ok {
		t.Fatalf("plaintext credential rejected: ok=%v err=%v", ok, err)
	}
	// 首字符随机，固定换成 "0" 有 1/16 概率与原值相同；换成确定不同的字符。
	wrong := "0" + created.Key[1:]
	if created.Key[0] == '0' {
		wrong = "1" + created.Key[1:]
	}
	if ok, err := s.HasAPIKey(wrong); err != nil || ok {
		t.Fatalf("wrong credential accepted: ok=%v err=%v", ok, err)
	}
	var stored string
	if err := s.db.QueryRow("SELECT key FROM api_keys WHERE key_hash=?", created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == created.Key {
		t.Fatal("plaintext key still stored")
	}
	if err := s.DeleteAPIKey(created.Key); err != nil {
		t.Fatalf("delete by plaintext: %v", err)
	}
	if ok, _ := s.HasAPIKey(created.Key); ok {
		t.Fatal("deleted key still valid")
	}
}

func TestMigrationBackfillsLegacyAPIKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-keys.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 回退到迁移 3，写入明文密钥后重开：迁移 4 必须改写为摘要且保留可用性。
	if _, err := s.db.Exec("DELETE FROM schema_migrations WHERE version=4"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DROP INDEX IF EXISTS idx_api_keys_hash",
		"ALTER TABLE api_keys DROP COLUMN key_hash",
		"ALTER TABLE api_keys DROP COLUMN key_prefix",
		"INSERT INTO api_keys(key,name,created_at) VALUES('legacy-plain-credential','legacy','t0')",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	want := apiKeyHash("legacy-plain-credential")
	var key, hash, prefix string
	if err := s.db.QueryRow("SELECT key,COALESCE(key_hash,''),COALESCE(key_prefix,'') FROM api_keys WHERE name='legacy'").Scan(&key, &hash, &prefix); err != nil {
		t.Fatal(err)
	}
	if key != want || hash != want || prefix != "legacy-plain" {
		t.Fatalf("backfill key=%q hash=%q prefix=%q", key, hash, prefix)
	}
	if ok, err := s.HasAPIKey("legacy-plain-credential"); err != nil || !ok {
		t.Fatalf("legacy credential rejected after migration: ok=%v err=%v", ok, err)
	}
}
