package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func identityUser(t *testing.T, s *Store) User {
	t.Helper()
	if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser("viewer", "old-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func authenticatedIdentity(t *testing.T, s *Store, u User) User {
	t.Helper()
	got, ok, err := s.AuthenticateUser(u.Name, "old-password-1")
	if err != nil || !ok {
		t.Fatalf("authenticate ok=%v err=%v", ok, err)
	}
	return got
}

func TestIdentityRejectsLatePasswordTokens(t *testing.T) {
	for _, mode := range []string{"password", "revoke-user", "revoke-all", "delete"} {
		t.Run(mode, func(t *testing.T) {
			s := openUsersStore(t)
			u := identityUser(t, s)
			authenticated := authenticatedIdentity(t, s, u)
			if err := s.SaveAccessTokenForAuthenticatedUser("before", authenticated, time.Time{}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "password":
				err = s.SetUserPassword(u.ID, "new-password-1")
			case "revoke-user":
				err = s.RevokeUserAccessTokens(u.ID)
			case "revoke-all":
				_, err = s.RevokeAllAccessTokens()
			case "delete":
				err = s.DeleteUser(u.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SaveAccessTokenForAuthenticatedUser("late", authenticated, time.Time{}); !errors.Is(err, ErrAuthenticationChanged) {
				t.Fatalf("late issuance err=%v", err)
			}
			for _, token := range []string{"before", "late"} {
				if _, valid, err := s.ValidateAccessTokenUser(token, time.Now(), 0); err != nil || valid {
					t.Fatalf("%s valid=%v err=%v", token, valid, err)
				}
			}
			if mode != "delete" {
				password := "old-password-1"
				if mode == "password" {
					password = "new-password-1"
				}
				current, ok, err := s.AuthenticateUser(u.Name, password)
				if err != nil || !ok {
					t.Fatal("current password rejected", err)
				}
				if err := s.SaveAccessTokenForAuthenticatedUser("current", current, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, valid, err := s.ValidateAccessTokenUser("current", time.Now(), 0); err != nil || !valid {
					t.Fatalf("fresh valid=%v err=%v", valid, err)
				}
			}
		})
	}
}

func TestIdentityNeverReusesDeletedAccount(t *testing.T) {
	s := openUsersStore(t)
	u := identityUser(t, s)
	authenticated := authenticatedIdentity(t, s, u)
	if err := s.RecordLoginEvent(u.ID, u.Name, "127.0.0.1", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForUser("orphan", u.ID, time.Time{}); !errors.Is(err, ErrAuthenticationChanged) {
		t.Fatalf("orphan issuance err=%v", err)
	}
	next, err := s.CreateUser("replacement-admin", "new-password-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID <= u.ID {
		t.Fatalf("reused identity: old=%d next=%d", u.ID, next.ID)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("late", authenticated, time.Time{}); !errors.Is(err, ErrAuthenticationChanged) {
		t.Fatal(err)
	}
	last, err := s.UserLastLogin()
	if err != nil {
		t.Fatal(err)
	}
	if last[next.ID] != "" {
		t.Fatal("replacement inherited login history")
	}
	if err := s.DeleteUser(next.ID); err != nil {
		t.Fatal(err)
	}
	path := ""
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	newest, err := reopened.CreateUser("newest", "newest-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if newest.ID <= next.ID {
		t.Fatalf("reused after reopen: old=%d newest=%d", next.ID, newest.ID)
	}
}

func TestIdentityPasswordAndRevocationRollbackTogether(t *testing.T) {
	s := openUsersStore(t)
	u := identityUser(t, s)
	authenticated := authenticatedIdentity(t, s, u)
	if err := s.SaveAccessTokenForAuthenticatedUser("keep", authenticated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_identity_revoke BEFORE DELETE ON access_tokens BEGIN SELECT RAISE(ABORT,'injected revoke failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPassword(u.ID, "new-password-1"); err == nil {
		t.Fatal("injected failure ignored")
	}
	if _, ok, err := s.AuthenticateUser(u.Name, "old-password-1"); err != nil || !ok {
		t.Fatalf("password partially committed: ok=%v err=%v", ok, err)
	}
	if _, valid, err := s.ValidateAccessTokenUser("keep", time.Now(), 0); err != nil || !valid {
		t.Fatalf("generation partially committed: valid=%v err=%v", valid, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_identity_revoke"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPassword(u.ID, "reset-password-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAuthenticatedUserPassword(authenticated, "stale-password-1"); !errors.Is(err, ErrAuthenticationChanged) {
		t.Fatalf("stale password overwrite err=%v", err)
	}
}

func TestIdentityConcurrentLastAdminProtection(t *testing.T) {
	for _, mode := range []string{"demote", "delete"} {
		t.Run(mode, func(t *testing.T) {
			s := openUsersStore(t)
			if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
				t.Fatal(err)
			}
			other, err := s.CreateUser("second", "second-password-1", true)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, op := range []func() error{func() error { return s.SetUserAdmin(1, false) }, func() error {
				if mode == "delete" {
					return s.DeleteUser(other.ID)
				}
				return s.SetUserAdmin(other.ID, false)
			}} {
				wg.Add(1)
				go func(op func() error) { defer wg.Done(); <-start; results <- op() }(op)
			}
			close(start)
			wg.Wait()
			close(results)
			success, protected := 0, 0
			for err := range results {
				if err == nil {
					success++
				} else if errors.Is(err, ErrLastAdmin) {
					protected++
				} else {
					t.Fatal(err)
				}
			}
			var admins int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin=1").Scan(&admins); err != nil {
				t.Fatal(err)
			}
			if success != 1 || protected != 1 || admins != 1 {
				t.Fatalf("success=%d protected=%d admins=%d", success, protected, admins)
			}
		})
	}
}

func TestIdentityTokenGenerationAndLogoutIsolation(t *testing.T) {
	s := openUsersStore(t)
	u := identityUser(t, s)
	authenticated := authenticatedIdentity(t, s, u)
	key, err := s.CreateAPIKeyForUser("keep-key", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"one", "two"} {
		if err := s.SaveAccessTokenForAuthenticatedUser(token, authenticated, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteAccessToken("one"); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := s.ValidateAccessTokenUser("two", time.Now(), 0); err != nil || !valid {
		t.Fatalf("other session revoked valid=%v err=%v", valid, err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("after-logout", authenticated, time.Time{}); err != nil {
		t.Fatal("logout changed account generation", err)
	}
	if _, err := s.db.Exec("UPDATE users SET auth_generation=auth_generation+1 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := s.ValidateAccessTokenUser("two", time.Now(), 0); err != nil || valid {
		t.Fatalf("stale generation valid=%v err=%v", valid, err)
	}
	if id, valid, err := s.APIKeyUser(key.Key); err != nil || !valid || id != u.ID {
		t.Fatalf("API key affected id=%d valid=%v err=%v", id, valid, err)
	}
}

// Build an actual pre-14 database rather than altering the new canonical schema.
func preIdentityFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre14.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := applyBaseline(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrationLedgerSchema); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	checks := []string{baselineChecksum(), migration2Checksum(), migration3Checksum(), migration4Checksum(), migration5Checksum(), migration6Checksum(), migration7Checksum(), migration8Checksum(), migration9Checksum(), migration10Checksum(), migration11Checksum(), migration12Checksum(), migration13Checksum()}
	for _, apply := range []func(*sql.Tx) error{applyMigration2, applyMigration3, applyMigration4, applyMigration5, applyMigration6, applyMigration7, applyMigration8, applyMigration9, applyMigration10, applyMigration11, applyMigration12, applyMigration13} {
		if err := apply(tx); err != nil {
			t.Fatal(err)
		}
	}
	for i, checksum := range checks {
		if _, err := tx.Exec("INSERT INTO schema_migrations VALUES(?,?,?)", i+1, checksum, "t0"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO users(id,name,password_hash,is_admin,created_at,updated_at) VALUES(1,'admin','hash',1,'t0','t0'),(4,'viewer','hash',0,'t0','t0');
 INSERT INTO libraries(id,name,path) VALUES(1,'library','/identity-library');
 INSERT INTO user_libraries(user_id,library_id) VALUES(4,1);
 INSERT INTO login_events(user_id,name,ip,success,created_at) VALUES(20,'deleted','127.0.0.1',1,'t0');`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO access_tokens(token,created_at,user_id) VALUES(?,'t0',4),(?,'t0',30)", tokenHash("retained"), tokenHash("orphan")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func TestIdentityMigrationPreservesGrantsAndHistoricalIDs(t *testing.T) {
	_, path := preIdentityFixture(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids, err := s.UserLibraryIDs(4)
	if err != nil || len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("lost grants: %v %v", ids, err)
	}
	if id, valid, err := s.ValidateAccessTokenUser("retained", time.Now(), 0); err != nil || !valid || id != 4 {
		t.Fatalf("retained token id=%d valid=%v err=%v", id, valid, err)
	}
	if _, valid, err := s.ValidateAccessTokenUser("orphan", time.Now(), 0); err != nil || valid {
		t.Fatalf("orphan token valid=%v err=%v", valid, err)
	}
	u, err := s.CreateUser("new-user", "new-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID <= 30 {
		t.Fatalf("historical id reused: %d", u.ID)
	}
}

func TestIdentityMigrationFailureRollsBackParentAndGrants(t *testing.T) {
	db, path := preIdentityFixture(t)
	if _, err := db.Exec(`CREATE TRIGGER reject_identity_ledger BEFORE INSERT ON schema_migrations WHEN NEW.version=14 BEGIN SELECT RAISE(ABORT,'injected identity ledger failure'); END`); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("migration ignored injected failure")
	}
	cols, err := tableColumns(db, "users")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cols["auth_generation"]; exists {
		t.Fatal("failed migration committed new parent")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM user_libraries WHERE user_id=4 AND library_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost grants: %d %v", count, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_identity_ledger"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal("retry failed", err)
	}
	s.Close()
}
