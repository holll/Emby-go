package store

import (
	"errors"
	"testing"
	"time"
)

func TestAdminUserTransactionsAuthorityAndRollback(t *testing.T) {
	s := openUsersStore(t)
	if err := s.InitializeAdministrator("owner", "password-1234"); err != nil {
		t.Fatal(err)
	}
	ownerProof := fixtureRequestProof(t, s, 1, "password-1234")
	actor, err := s.AdminCreateUser(1, "actor", "password-1234", true, nil, ownerProof)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("actor-proof", fixtureAuthenticatedUser(t, s, actor.ID, "password-1234"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	proof, err := s.RequestCredentialUser("actor-proof", actor.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("owner-proof", fixtureAuthenticatedUser(t, s, 1, "password-1234"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	owner, err := s.RequestCredentialUser("owner-proof", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.AdminCreateUser(actor.ID, "target", "password-1234", false, nil, proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccessTokenForAuthenticatedUser("target-session", fixtureAuthenticatedUser(t, s, target.ID, "password-1234"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	name, password, admin, libs := "changed-target", "new-password-1234", true, []int64{-123}
	update := AdminUserUpdate{Name: &name, Password: &password, IsAdmin: &admin, LibraryIDs: &libs}
	if err := s.AdminUpdateUser(actor.ID, target.ID, update, proof); err == nil {
		t.Fatal("missing library must roll back all fields")
	}
	got, err := s.UserByID(target.ID)
	if err != nil || got.Name != target.Name || got.IsAdmin {
		t.Fatalf("partial update: %+v %v", got, err)
	}
	if _, ok, err := s.AuthenticateUser(target.Name, "password-1234"); err != nil || !ok {
		t.Fatalf("password partially changed: %v", err)
	}
	if _, ok, err := s.ValidateAccessTokenUser("target-session", time.Now(), 0); err != nil || !ok {
		t.Fatalf("session partially revoked: %v", err)
	}
	if _, err := s.AdminCreateUser(actor.ID, "rollback-user", "password-1234", false, libs, proof); err == nil {
		t.Fatal("invalid create grants accepted")
	}
	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Name == "rollback-user" {
			t.Fatal("partial create")
		}
	}
	update.LibraryIDs = nil
	if err := s.AdminUpdateUser(actor.ID, target.ID, update, proof); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ValidateAccessTokenUser("target-session", time.Now(), 0); err != nil || ok {
		t.Fatalf("password update did not revoke session: %v", err)
	}
	for _, actorID := range []int64{0, actor.ID} {
		if actorID == actor.ID {
			no := false
			if err := s.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &no}, owner); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.AdminCreateUser(actorID, "denied-create", "password-1234", true, nil, proof); !errors.Is(err, ErrAdminGenerationStale) {
			t.Fatalf("create err=%v", err)
		}
		if err := s.AdminUpdateUser(actorID, target.ID, AdminUserUpdate{Name: &name}, proof); !errors.Is(err, ErrAdminGenerationStale) {
			t.Fatalf("update err=%v", err)
		}
		if err := s.AdminDeleteUser(actorID, target.ID, proof); !errors.Is(err, ErrAdminGenerationStale) {
			t.Fatalf("delete err=%v", err)
		}
	}
	if err := s.AdminDeleteUser(1, target.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminDeleteUser(1, 1, owner); !errors.Is(err, ErrProtectedUser) {
		t.Fatalf("owner deletion err=%v", err)
	}
	no := false
	if err := s.AdminUpdateUser(1, 1, AdminUserUpdate{IsAdmin: &no}, owner); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin demotion err=%v", err)
	}
}
