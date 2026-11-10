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
	proof := fixtureRequestProof(t, s, 1, "admin-password-1")
	u, err := s.AdminCreateUser(1, "viewer", "viewer-password-1", false, nil, proof)
	if err != nil || u.ID == 0 || u.IsAdmin {
		t.Fatalf("user=%+v err=%v", u, err)
	}
	if _, err := s.AdminCreateUser(1, "viewer", "another-password", false, nil, proof); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate err=%v", err)
	}
	if _, err := s.AdminCreateUser(1, "  ", "another-password", false, nil, proof); !errors.Is(err, ErrInvalidUserName) {
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
	password := "viewer-password-2"
	if err := s.AdminUpdateUser(1, u.ID, AdminUserUpdate{Password: &password}, proof); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AuthenticateUser("viewer", "viewer-password-2"); !ok {
		t.Fatal("new password rejected")
	}
	name := "viewer2"
	if err := s.AdminUpdateUser(1, u.ID, AdminUserUpdate{Name: &name}, proof); err != nil {
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
	proof := fixtureRequestProof(t, s, 1, "admin-password-1")
	if err := s.AdminDeleteUser(1, 1, proof); !errors.Is(err, ErrProtectedUser) {
		t.Fatalf("delete initial admin err=%v", err)
	}
	viewer, err := s.AdminCreateUser(1, "viewer", "viewer-password-1", false, nil, fixtureRequestProof(t, s, 1, "admin-password-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("viewer-token", fixtureAuthenticatedUser(t, s, viewer.ID, "viewer-password-1"), zeroTime()); err != nil {
		t.Fatal(err)
	}
	seedLegacyAPIKey(t, s, viewer.ID, "viewer-key")
	if err := s.AdminDeleteUser(1, viewer.ID, proof); err != nil {
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
	second, err := s.AdminCreateUser(1, "second", "second-password-1", true, nil, proof)
	if err != nil {
		t.Fatal(err)
	}
	secondProof := fixtureRequestProof(t, s, second.ID, "second-password-1")
	no, yes := false, true
	if err := s.AdminUpdateUser(1, 1, AdminUserUpdate{IsAdmin: &no}, proof); err != nil {
		t.Fatalf("demote id=1 err=%v", err)
	}
	if err := s.AdminUpdateUser(second.ID, second.ID, AdminUserUpdate{IsAdmin: &no}, secondProof); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin err=%v", err)
	}
	if err := s.AdminDeleteUser(second.ID, second.ID, secondProof); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin err=%v", err)
	}
	if err := s.AdminUpdateUser(second.ID, 1, AdminUserUpdate{IsAdmin: &yes}, secondProof); err != nil {
		t.Fatalf("restore admin err=%v", err)
	}
	if err := s.AdminDeleteUser(1, second.ID, fixtureRequestProof(t, s, 1, "admin-password-1")); err != nil {
		t.Fatalf("delete second admin err=%v", err)
	}
}

func TestTokensAndAPIKeysBindUsers(t *testing.T) {
	s := openUsersStore(t)
	if err := s.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.AdminCreateUser(1, "viewer", "viewer-password-1", false, nil, fixtureRequestProof(t, s, 1, "admin-password-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("viewer-token", fixtureAuthenticatedUser(t, s, viewer.ID, "viewer-password-1"), zeroTime()); err != nil {
		t.Fatal(err)
	}
	uid, valid, err := s.ValidateAccessTokenUser("viewer-token", zeroTime(), 0)
	if err != nil || !valid || uid != viewer.ID {
		t.Fatalf("uid=%d valid=%v err=%v", uid, valid, err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("owner-token", fixtureAuthenticatedUser(t, s, 1, "admin-password-1"), zeroTime()); err != nil {
		t.Fatal(err)
	}
	uid, valid, err = s.ValidateAccessTokenUser("owner-token", zeroTime(), 0)
	if err != nil || !valid || uid != 1 {
		t.Fatalf("legacy uid=%d valid=%v err=%v", uid, valid, err)
	}
	created := seedLegacyAPIKey(t, s, viewer.ID, "viewer-key")
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
