package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Each revocation commits through a separate Store after request authentication
// and before admission/commit. No scheduler timing or sleep is involved.
func TestStabilityPermissionsCredentialCommitMatrix(t *testing.T) {
	for _, credential := range []string{"session", "apikey"} {
		for _, revoke := range []string{"logout", "password", "role-ABA", "delete-user", "rotate-key", "delete-key", "expire-session", "role-change", "revoke-all", "unchanged"} {
			t.Run(credential+"/"+revoke, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "permissions.db")
				s, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				other, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				// Only seed setup accounts; authentication and the cross-Store
				// credential commit/revocation paths below remain real.
				seedFixtureUser(t, s, "owner", "password-1234", true)
				actor := seedFixtureUser(t, s, "actor", "password-1234", true)
				target, err := s.AdminCreateAPIKey(fixtureRequestProof(t, s, 1, "password-1234"), "target")
				if err != nil {
					t.Fatal(err)
				}
				source, err := s.AdminCreateAPIKey(fixtureRequestProof(t, s, actor.ID, "password-1234"), "source")
				if err != nil {
					t.Fatal(err)
				}
				const session = "stability-session"
				if err := s.SaveAccessTokenForAuthenticatedUser(session, fixtureAuthenticatedUser(t, s, actor.ID, "password-1234"), time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				token := session
				if credential == "apikey" {
					token = source.Key
				}
				proof, err := s.RequestCredentialUser(token, actor.ID, credential == "session")
				if err != nil {
					t.Fatal(err)
				}
				authenticated, valid, err := s.AuthenticateUser("actor", "password-1234")
				if err != nil || !valid {
					t.Fatal(err)
				}
				owner := fixtureRequestProof(t, other, 1, "password-1234")
				no, yes := false, true
				switch revoke {
				case "logout":
					err = other.DeleteAccessToken(session)
				case "password":
					password := "changed-password-1234"
					err = other.AdminUpdateUser(1, actor.ID, AdminUserUpdate{Password: &password}, owner)
				case "role-ABA":
					err = other.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &no}, owner)
					if err == nil {
						err = other.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &yes}, owner)
					}
				case "delete-user":
					err = other.AdminDeleteUser(1, actor.ID, owner)
				case "rotate-key":
					_, err = other.RotateAPIKeyForRequest(fixtureAuthenticatedUser(t, other, 1, "password-1234"), owner, source.ID, source.Version)
				case "delete-key":
					err = other.AdminDeleteAPIKey(owner, source.ID, source.Version)
				case "expire-session":
					_, err = other.db.Exec("UPDATE access_tokens SET expires_at=? WHERE token=?", time.Now().Add(-time.Hour).Unix(), tokenHash(session))
				case "role-change":
					err = other.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &no}, owner)
				case "revoke-all":
					_, err = other.AdminRevokeAllAccessTokens(owner)
				}
				if err != nil {
					t.Fatal(err)
				}
				stale := revoke != "unchanged" && !(credential == "apikey" && (revoke == "logout" || revoke == "expire-session")) && !(credential == "session" && (revoke == "rotate-key" || revoke == "delete-key"))
				operations := []struct {
					name string
					run  func() error
				}{
					{"admit", func() error { return s.AdmitAdminOperation(proof) }},
					{"create-key", func() error { _, err := s.AdminCreateAPIKey(proof, "new-key"); return err }},
					{"delete-key", func() error { return s.AdminDeleteAPIKey(proof, target.ID, 1) }},
				}
				for _, op := range operations {
					err := op.run()
					if stale && !errors.Is(err, ErrAdminGenerationStale) {
						t.Fatalf("%s stale error=%v", op.name, err)
					}
					if !stale && err != nil {
						t.Fatalf("%s fresh error=%v", op.name, err)
					}
				}
				if stale {
					if _, err := s.AdminRevokeAllAccessTokens(proof); !errors.Is(err, ErrAdminGenerationStale) {
						t.Fatalf("revoke-all stale error=%v", err)
					}
					if credential == "session" {
						if _, err := s.RotateAPIKeyForRequest(authenticated, proof, target.ID, 1); !errors.Is(err, ErrAdminGenerationStale) {
							t.Fatalf("rotate stale error=%v", err)
						}
					}
					if _, ok, err := s.APIKeyUser(target.Key); err != nil || !ok {
						t.Fatalf("target changed: ok=%v err=%v", ok, err)
					}
				}
				// Long-term API keys are not revoked by a password change or global session
				// revocation. A new request gets current generation/role; stale writes lose.
				if revoke == "password" || revoke == "revoke-all" || revoke == "logout" || revoke == "role-ABA" || revoke == "role-change" || revoke == "expire-session" {
					fresh, err := s.RequestCredentialUser(source.Key, actor.ID, false)
					if err != nil || fresh.IsAdmin != (revoke != "role-change") {
						t.Fatalf("long-term key lost or role stale: %+v %v", fresh, err)
					}
					err = s.AdmitAdminOperation(fresh)
					if revoke == "role-change" {
						if !errors.Is(err, ErrAdminGenerationStale) {
							t.Fatalf("demoted key admitted: %v", err)
						}
					} else if err != nil {
						t.Fatalf("fresh key admission: %v", err)
					}
				}
			})
		}
	}
}

func TestStabilityPermissionsRoleTracksLongTermKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedFixtureUser(t, s, "owner", "password-1234", true)
	actor := seedFixtureUser(t, s, "actor", "password-1234", true)
	key, err := s.AdminCreateAPIKey(fixtureRequestProof(t, s, actor.ID, "password-1234"), "script")
	if err != nil {
		t.Fatal(err)
	}
	owner := fixtureRequestProof(t, s, 1, "password-1234")
	no, yes := false, true
	if err := s.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &no}, owner); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.RequestCredentialUser(key.Key, actor.ID, false)
	if err != nil || viewer.IsAdmin {
		t.Fatalf("key role after demotion: %+v %v", viewer, err)
	}
	if err := s.AdmitAdminOperation(viewer); !errors.Is(err, ErrAdminGenerationStale) {
		t.Fatalf("viewer admitted: %v", err)
	}
	if err := s.AdminUpdateUser(1, actor.ID, AdminUserUpdate{IsAdmin: &yes}, owner); err != nil {
		t.Fatal(err)
	}
	admin, err := s.RequestCredentialUser(key.Key, actor.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitAdminOperation(admin); err != nil {
		t.Fatal(err)
	}
}
