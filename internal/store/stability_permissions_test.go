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
		for _, revoke := range []string{"logout", "password", "role-ABA", "delete-user", "rotate-key", "delete-key", "expire-session", "revoke-user", "revoke-all", "unchanged"} {
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
				target, err := s.CreateAPIKeyForUser("target", 1)
				if err != nil {
					t.Fatal(err)
				}
				source, err := s.CreateAPIKeyForUser("source", actor.ID)
				if err != nil {
					t.Fatal(err)
				}
				const session = "stability-session"
				if err := s.SaveAccessTokenForUser(session, actor.ID, time.Now().Add(time.Hour)); err != nil {
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
				switch revoke {
				case "logout":
					err = other.DeleteAccessToken(session)
				case "password":
					err = other.SetUserPassword(actor.ID, "changed-password-1234")
				case "role-ABA":
					err = other.SetUserAdmin(actor.ID, false)
					if err == nil {
						err = other.SetUserAdmin(actor.ID, true)
					}
				case "delete-user":
					err = other.DeleteUser(actor.ID)
				case "rotate-key":
					_, err = other.RotateAPIKey(authenticated, source.ID, 1)
				case "delete-key":
					err = other.DeleteAPIKey(source.ID)
				case "expire-session":
					_, err = other.db.Exec("UPDATE access_tokens SET expires_at=? WHERE token=?", time.Now().Add(-time.Hour).Unix(), tokenHash(session))
				case "revoke-user":
					err = other.RevokeUserAccessTokens(actor.ID)
				case "revoke-all":
					_, err = other.RevokeAllAccessTokens()
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
					if ok, err := s.HasAPIKey(target.Key); err != nil || !ok {
						t.Fatalf("target changed: ok=%v err=%v", ok, err)
					}
				}
				// Long-term API keys are not revoked by a password change or global session
				// revocation. A new request gets current generation/role; stale writes lose.
				if revoke == "password" || revoke == "revoke-all" || revoke == "logout" || revoke == "role-ABA" || revoke == "revoke-user" || revoke == "expire-session" {
					fresh, err := s.RequestCredentialUser(source.Key, actor.ID, false)
					if err != nil || !fresh.IsAdmin {
						t.Fatalf("long-term key lost: %+v %v", fresh, err)
					}
					if err := s.AdmitAdminOperation(fresh); err != nil {
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
	key, err := s.CreateAPIKeyForUser("script", actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserAdmin(actor.ID, false); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.RequestCredentialUser(key.Key, actor.ID, false)
	if err != nil || viewer.IsAdmin {
		t.Fatalf("key role after demotion: %+v %v", viewer, err)
	}
	if err := s.AdmitAdminOperation(viewer); !errors.Is(err, ErrAdminGenerationStale) {
		t.Fatalf("viewer admitted: %v", err)
	}
	if err := s.SetUserAdmin(actor.ID, true); err != nil {
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
