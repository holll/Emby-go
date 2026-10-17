package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAccessTokenExpiryValidation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now()
	const ttl = 30 * 24 * time.Hour

	if err := s.SaveAccessTokenExpiring("exp-soon", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// ttl=0：只校验绝对过期，不触发滑动续期（续期单独测）。
	if ok, err := s.ValidateAccessToken("exp-soon", now, 0); err != nil || !ok {
		t.Fatalf("valid session rejected: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ValidateAccessToken("exp-soon", now.Add(3*time.Hour), 0); err != nil || ok {
		t.Fatalf("expired session accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := s.HasAccessToken("exp-soon"); err != nil || ok {
		t.Fatalf("expired session not deleted: ok=%v err=%v", ok, err)
	}

	// 未写 expires_at 的历史/夹具行视为不过期。
	if err := s.SaveAccessToken("legacy"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateAccessToken("legacy", now.Add(100000*time.Hour), ttl); err != nil || !ok {
		t.Fatalf("legacy session rejected: ok=%v err=%v", ok, err)
	}

	// 剩余不足 TTL-24h 时滑动续期。
	if err := s.SaveAccessTokenExpiring("renew", now.Add(10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateAccessToken("renew", now, ttl); err != nil || !ok {
		t.Fatalf("renew session rejected: ok=%v err=%v", ok, err)
	}
	var renewed int64
	if err := s.db.QueryRow("SELECT expires_at FROM access_tokens WHERE token='renew'").Scan(&renewed); err != nil {
		t.Fatal(err)
	}
	if renewed < now.Add(29*24*time.Hour).Unix() {
		t.Fatalf("session not renewed: expires_at=%d", renewed)
	}

	// 剩余时间充足时不写库。
	stableExpiry := now.Add(29*24*time.Hour + time.Hour)
	if err := s.SaveAccessTokenExpiring("stable", stableExpiry); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateAccessToken("stable", now, ttl); err != nil || !ok {
		t.Fatalf("stable session rejected: ok=%v err=%v", ok, err)
	}
	var stable int64
	if err := s.db.QueryRow("SELECT expires_at FROM access_tokens WHERE token='stable'").Scan(&stable); err != nil {
		t.Fatal(err)
	}
	if stable != stableExpiry.Unix() {
		t.Fatalf("stable session rewritten: got=%d want=%d", stable, stableExpiry.Unix())
	}

	if err := s.SaveAccessTokenExpiring("zero-ttl", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateAccessToken("zero-ttl", now.Add(100000*time.Hour), 0); err != nil || !ok {
		t.Fatalf("zero ttl session rejected: ok=%v err=%v", ok, err)
	}
}

func TestRevokeAllAccessTokens(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, token := range []string{"a", "b", "c"} {
		if err := s.SaveAccessToken(token); err != nil {
			t.Fatal(err)
		}
	}
	revoked, err := s.RevokeAllAccessTokens()
	if err != nil || revoked != 3 {
		t.Fatalf("revoke all: revoked=%d err=%v", revoked, err)
	}
	for _, token := range []string{"a", "b", "c"} {
		if ok, err := s.HasAccessToken(token); err != nil || ok {
			t.Fatalf("token %s survived revoke: ok=%v err=%v", token, ok, err)
		}
	}
}

func TestMigrationBackfillsLegacySessionExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec("INSERT INTO access_tokens(token,created_at) VALUES(?,?)", "legacy-row", created.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// 回退到迁移 2：删列并移除版本记录，重开时迁移 3 必须回填。
	if _, err := s.db.Exec("DELETE FROM schema_migrations WHERE version=3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE access_tokens DROP COLUMN expires_at"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var expires int64
	if err := s.db.QueryRow("SELECT expires_at FROM access_tokens WHERE token='legacy-row'").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if want := created.Add(30 * 24 * time.Hour).Unix(); expires != want {
		t.Fatalf("backfilled expiry=%d want=%d", expires, want)
	}
}
