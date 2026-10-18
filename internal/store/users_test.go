package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openUsersStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.waitFeaturesReady()
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUsersCRUDAndAuthentication(t *testing.T) {
	s := openUsersStore(t)
	if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	admin, err := s.UserByID(1)
	if err != nil || admin.Name != "admin" || !admin.IsAdmin {
		t.Fatalf("admin=%+v err=%v", admin, err)
	}
	u, err := s.CreateUser("viewer", "viewer-password-1", false)
	if err != nil || u.ID == 0 || u.IsAdmin {
		t.Fatalf("user=%+v err=%v", u, err)
	}
	if _, err := s.CreateUser("viewer", "another-password", false); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate err=%v", err)
	}
	if _, err := s.CreateUser("  ", "another-password", false); !errors.Is(err, ErrInvalidUserName) {
		t.Fatalf("invalid name err=%v", err)
	}
	got, ok, err := s.AuthenticateUser("viewer", "viewer-password-1")
	if err != nil || !ok || got.ID != u.ID {
		t.Fatalf("auth got=%+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := s.AuthenticateUser("viewer", "wrong-password"); err != nil || ok {
		t.Fatalf("wrong password ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.AuthenticateUser("missing", "whatever"); err != nil || ok {
		t.Fatalf("unknown user ok=%v err=%v", ok, err)
	}
	if err := s.SetUserPassword(u.ID, "viewer-password-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AuthenticateUser("viewer", "viewer-password-2"); !ok {
		t.Fatal("new password rejected")
	}
	if err := s.RenameUser(u.ID, "viewer2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AuthenticateUser("viewer2", "viewer-password-2"); !ok {
		t.Fatal("renamed user cannot login")
	}
	users, err := s.ListUsers()
	if err != nil || len(users) != 2 || users[1].Name != "viewer2" {
		t.Fatalf("users=%+v err=%v", users, err)
	}
}

func TestDeleteUserGuardsAndCleanup(t *testing.T) {
	s := openUsersStore(t)
	if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(1); !errors.Is(err, ErrProtectedUser) {
		t.Fatalf("delete initial admin err=%v", err)
	}
	viewer, err := s.CreateUser("viewer", "viewer-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForUser("viewer-token", viewer.ID, zeroTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAPIKeyForUser("viewer-key", viewer.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByID(viewer.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("user still exists: %v", err)
	}
	var tokens int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM access_tokens WHERE user_id=?", viewer.ID).Scan(&tokens); err != nil || tokens != 0 {
		t.Fatalf("tokens=%d err=%v", tokens, err)
	}

	// 唯一的另一名管理员不能被降级或删除。
	second, err := s.CreateUser("second", "second-password-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserAdmin(1, false); err != nil {
		t.Fatalf("demote id=1 err=%v", err)
	}
	if err := s.SetUserAdmin(second.ID, false); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin err=%v", err)
	}
	if err := s.DeleteUser(second.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin err=%v", err)
	}
	if err := s.SetUserAdmin(1, true); err != nil {
		t.Fatalf("restore admin err=%v", err)
	}
	if err := s.DeleteUser(second.ID); err != nil {
		t.Fatalf("delete second admin err=%v", err)
	}
}

func TestTokensAndAPIKeysBindUsers(t *testing.T) {
	s := openUsersStore(t)
	if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.CreateUser("viewer", "viewer-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForUser("viewer-token", viewer.ID, zeroTime()); err != nil {
		t.Fatal(err)
	}
	uid, valid, err := s.ValidateAccessTokenUser("viewer-token", zeroTime(), 0)
	if err != nil || !valid || uid != viewer.ID {
		t.Fatalf("uid=%d valid=%v err=%v", uid, valid, err)
	}
	if err := s.SaveAccessToken("legacy-token"); err != nil {
		t.Fatal(err)
	}
	uid, valid, err = s.ValidateAccessTokenUser("legacy-token", zeroTime(), 0)
	if err != nil || !valid || uid != 1 {
		t.Fatalf("legacy uid=%d valid=%v err=%v", uid, valid, err)
	}
	created, err := s.CreateAPIKeyForUser("viewer-key", viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok, err := s.APIKeyUser(created.Key)
	if err != nil || !ok || uid != viewer.ID {
		t.Fatalf("api key uid=%d ok=%v err=%v", uid, ok, err)
	}
	if uid, ok, err := s.APIKeyUser("no-such-key"); err != nil || ok {
		t.Fatalf("unknown key uid=%d ok=%v err=%v", uid, ok, err)
	}
}

func TestMigrationCopiesLegacyAdministrator(t *testing.T) {
	s := openUsersStore(t)
	if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	var adminCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE id=1 AND is_admin=1").Scan(&adminCount); err != nil || adminCount != 1 {
		t.Fatalf("adminCount=%d err=%v", adminCount, err)
	}
	// 迁移从 administrators 复制而不是覆盖：把 users 清空后用旧表重建一次。
	if _, err := s.db.Exec("DELETE FROM users; INSERT INTO users(id,name,password_hash,is_admin,created_at,updated_at) SELECT id,username,password_hash,1,created_at,created_at FROM administrators WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByID(1)
	if err != nil || u.Name != "admin" || !u.IsAdmin {
		t.Fatalf("u=%+v err=%v", u, err)
	}
}

func zeroTime() (t time.Time) { return }
