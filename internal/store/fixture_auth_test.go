package store

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

var fixtureProofSequence atomic.Uint64

// Fixture proofs must originate from real authentication and persisted sessions;
// a UserByID snapshot alone cannot authorize production administrator mutations.
func fixtureAuthenticatedUser(t *testing.T, s *Store, id int64, password string) User {
	t.Helper()
	u, err := s.UserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	u, valid, err := s.AuthenticateUser(u.Name, password)
	if err != nil || !valid {
		t.Fatalf("fixture authentication user=%d valid=%v err=%v", id, valid, err)
	}
	return u
}

func fixtureRequestProof(t *testing.T, s *Store, id int64, password string) User {
	t.Helper()
	u := fixtureAuthenticatedUser(t, s, id, password)
	token := fmt.Sprintf("fixture-proof-%d-%d", id, fixtureProofSequence.Add(1))
	if err := s.SaveAccessTokenForAuthenticatedUser(token, u, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	proof, err := s.RequestCredentialUser(token, id, true)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

// Old databases may contain keys owned by viewers. Seed that historical state
// directly: the live AdminCreateAPIKey endpoint must not be weakened for fixtures.
func seedLegacyAPIKey(t *testing.T, s *Store, id int64, name string) APIKey {
	t.Helper()
	plaintext, digest, prefix, err := newAPIKeyCredential()
	if err != nil {
		t.Fatal(err)
	}
	stable, err := newAPIKeyStableID()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec("INSERT INTO api_keys(key,key_hash,key_prefix,name,created_at,user_id,stable_id,version) VALUES(?,?,?,?,?,?,?,1)", digest, digest, prefix, name, created, id, stable); err != nil {
		t.Fatal(err)
	}
	return APIKey{ID: APIKeyPublicID(stable), Key: plaintext, KeyPrefix: prefix, Name: name, CreatedAt: created, Version: 1}
}
