package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotateAPIKeyReplacesCredentialAtomically(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rotate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	admin := seedFixtureAdministrator(t, s, "admin", "correct-horse")
	actor := fixtureRequestProof(t, s, admin.ID, "correct-horse")
	created, err := s.AdminCreateAPIKey(fixtureRequestProof(t, s, admin.ID, "correct-horse"), "script")
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.RotatedAt != "" {
		t.Fatalf("new key version=%d rotated=%q", created.Version, created.RotatedAt)
	}
	rotated, err := s.RotateAPIKeyForRequest(actor, actor, created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != created.ID || rotated.Name != created.Name || rotated.CreatedAt != created.CreatedAt {
		t.Fatalf("rotation must keep record identity/name/created_at: %+v", rotated)
	}
	if rotated.Version != 2 || rotated.RotatedAt == "" || rotated.Key == created.Key || len(rotated.Key) != 48 {
		t.Fatalf("rotated=%+v", rotated)
	}
	if _, ok, err := s.APIKeyUser(created.Key); err != nil || ok {
		t.Fatalf("old key still valid: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.APIKeyUser(rotated.Key); err != nil || !ok {
		t.Fatalf("new key invalid: ok=%v err=%v", ok, err)
	}
	if uid, ok, err := s.APIKeyUser(rotated.Key); err != nil || !ok || uid != admin.ID {
		t.Fatalf("new key owner=%d ok=%v err=%v", uid, ok, err)
	}
	// 同版本再次轮转必须冲突，且新明文不能覆盖已提交版本。
	if _, err := s.RotateAPIKeyForRequest(actor, actor, created.ID, 1); !errors.Is(err, ErrAPIKeyVersionStale) {
		t.Fatalf("stale rotate err=%v", err)
	}
	if _, ok, _ := s.APIKeyUser(rotated.Key); !ok {
		t.Fatal("failed rotation must leave committed key valid")
	}
	if _, err := s.RotateAPIKeyForRequest(actor, actor, "ak_"+strings.Repeat("0", 32), 1); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("missing rotate err=%v", err)
	}
}

func TestRotateAPIKeyRequiresCurrentAdminGeneration(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rotate-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	admin := seedFixtureAdministrator(t, s, "admin", "correct-horse")
	actor := fixtureRequestProof(t, s, admin.ID, "correct-horse")
	key, err := s.AdminCreateAPIKey(fixtureRequestProof(t, s, admin.ID, "correct-horse"), "script")
	if err != nil {
		t.Fatal(err)
	}
	password := "changed-password"
	if err := s.AdminUpdateUser(admin.ID, admin.ID, AdminUserUpdate{Password: &password}, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateAPIKeyForRequest(actor, actor, key.ID, 1); !errors.Is(err, ErrAdminGenerationStale) {
		t.Fatalf("stale generation rotate err=%v", err)
	}
	viewer, err := s.AdminCreateUser(admin.ID, "viewer", "viewer-password", false, nil, fixtureRequestProof(t, s, admin.ID, "changed-password"))
	if err != nil {
		t.Fatal(err)
	}
	viewerActor := fixtureRequestProof(t, s, viewer.ID, "viewer-password")
	if _, err := s.RotateAPIKeyForRequest(viewerActor, viewerActor, key.ID, 1); !errors.Is(err, ErrAdminGenerationStale) {
		t.Fatalf("non-admin rotate err=%v", err)
	}
	if _, ok, _ := s.APIKeyUser(key.Key); !ok {
		t.Fatal("failed reauth must not invalidate key")
	}
	_ = viewer
}

func TestDeleteAPIKeyWithVersionPrecondition(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "delete-version.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	admin := seedFixtureAdministrator(t, s, "admin", "correct-horse")
	key, err := s.AdminCreateAPIKey(fixtureRequestProof(t, s, admin.ID, "correct-horse"), "script")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdminDeleteAPIKey(fixtureRequestProof(t, s, admin.ID, "correct-horse"), key.ID, 2); !errors.Is(err, ErrAPIKeyVersionStale) {
		t.Fatalf("delete stale err=%v", err)
	}
	if _, ok, _ := s.APIKeyUser(key.Key); !ok {
		t.Fatal("stale delete removed key")
	}
	if err := s.AdminDeleteAPIKey(fixtureRequestProof(t, s, admin.ID, "correct-horse"), key.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.APIKeyUser(key.Key); ok {
		t.Fatal("key survived delete")
	}
	if err := s.AdminDeleteAPIKey(fixtureRequestProof(t, s, admin.ID, "correct-horse"), key.ID, 1); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
}
