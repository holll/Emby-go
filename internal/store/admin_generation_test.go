package store

import (
	"errors"
	"testing"
	"time"
)

// A second Store commits changes between credential authentication and mutation.
// This also exercises API keys (which survive password/session revocation).
func TestAdminCredentialGenerationAcrossStores(t *testing.T) {
	for _, mode := range []string{"password", "revoke-user", "revoke-all", "logout", "role-ABA", "admin-role-ABA", "expiry", "key-delete", "key-password", "key-role-ABA"} {
		t.Run(mode, func(t *testing.T) {
			s := openUsersStore(t)
			if err := s.InitializeAdministrator("owner", "password-1234"); err != nil {
				t.Fatal(err)
			}
			actor, err := s.CreateUser("actor", "password-1234", true)
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.CreateUser("target", "password-1234", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SaveAccessTokenForUser("session", actor.ID, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			key, err := s.CreateAPIKeyForUser("actor-key", actor.ID)
			if err != nil {
				t.Fatal(err)
			}
			credential, session := "session", true
			if len(mode) > 4 && mode[:4] == "key-" {
				credential, session = key.Key, false
			}
			proof, err := s.RequestCredentialUser(credential, actor.ID, session)
			if err != nil {
				t.Fatal(err)
			}
			var path string
			if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
				t.Fatal(err)
			}
			other, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			switch mode {
			case "password", "key-password":
				err = other.SetUserPassword(actor.ID, "new-password-1234")
			case "revoke-user":
				err = other.RevokeUserAccessTokens(actor.ID)
			case "revoke-all":
				_, err = other.RevokeAllAccessTokens()
			case "logout":
				err = other.DeleteAccessToken("session")
			case "expiry":
				_, err = other.db.Exec("UPDATE access_tokens SET expires_at=? WHERE user_id=?", time.Now().Add(-time.Hour).Unix(), actor.ID)
			case "key-delete":
				err = other.DeleteAPIKey(key.ID)
			case "role-ABA", "key-role-ABA":
				err = other.SetUserAdmin(actor.ID, false)
				if err == nil {
					err = other.SetUserAdmin(actor.ID, true)
				}
			case "admin-role-ABA":
				if err = other.SaveAccessTokenForUser("owner-session", 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
				owner, e := other.RequestCredentialUser("owner-session", 1, true)
				if e != nil {
					t.Fatal(e)
				}
				no, yes := false, true
				err = other.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &no}, owner)
				if err == nil {
					err = other.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &yes}, owner)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			name, password, admin, libs := "injected-name", "injected-password", true, []int64{}
			if _, err := s.AdminCreateUser(actor.ID, "injected-create", "password-1234", true, nil, proof); !errors.Is(err, ErrAdminGenerationStale) {
				t.Fatalf("create err=%v", err)
			}
			if err := s.AdminUpdateUser(actor.ID, target.ID, AdminUserUpdate{Name: &name, Password: &password, IsAdmin: &admin, LibraryIDs: &libs}, proof); !errors.Is(err, ErrAdminGenerationStale) {
				t.Fatalf("update err=%v", err)
			}
			if err := s.AdminDeleteUser(actor.ID, target.ID, proof); !errors.Is(err, ErrAdminGenerationStale) {
				t.Fatalf("delete err=%v", err)
			}
			got, err := s.UserByID(target.ID)
			if err != nil || got.Name != target.Name || got.IsAdmin {
				t.Fatalf("partial write: %+v %v", got, err)
			}
			if _, ok, err := s.AuthenticateUser(target.Name, "password-1234"); err != nil || !ok {
				t.Fatalf("password changed: %v", err)
			}
			// Freshly authenticated API-key requests retain their independent lifetime.
			if mode == "key-password" || mode == "key-role-ABA" {
				fresh, err := s.RequestCredentialUser(key.Key, actor.ID, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.AdminUpdateUser(actor.ID, target.ID, AdminUserUpdate{Name: &name}, fresh); err != nil {
					t.Fatalf("fresh key rejected: %v", err)
				}
			}
		})
	}
}
