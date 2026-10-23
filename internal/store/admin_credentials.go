package store

import (
	"database/sql"
	"strings"
	"time"
)

// AdmitAdminOperation is a short, request-bound authorization barrier for work
// that cannot run in a SQLite transaction (workers, network or filesystem I/O).
// Revocation committed before admission wins. Admitted work may finish after
// revocation; callers must not hold the transaction across that work.
func (s *Store) AdmitAdminOperation(proof User) error {
	return s.adminUserTx(proof.ID, func(*sql.Tx) error { return nil }, proof)
}

func (s *Store) AdminCreateAPIKey(proof User, name string) (APIKey, error) {
	plaintext, digest, prefix, err := newAPIKeyCredential()
	if err != nil {
		return APIKey{}, err
	}
	stable, err := newAPIKeyStableID()
	if err != nil {
		return APIKey{}, err
	}
	key := APIKey{ID: APIKeyPublicID(stable), Key: plaintext, KeyPrefix: prefix, Name: strings.TrimSpace(name), CreatedAt: time.Now().UTC().Format(time.RFC3339), Version: 1}
	err = s.adminUserTx(proof.ID, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO api_keys(key,key_hash,key_prefix,name,created_at,user_id,stable_id,version) VALUES(?,?,?,?,?,?,?,1)", digest, digest, prefix, key.Name, key.CreatedAt, proof.ID, stable)
		return err
	}, proof)
	if err != nil {
		return APIKey{}, err
	}
	return key, nil
}

func (s *Store) AdminDeleteAPIKey(proof User, publicID string, expectedVersion int64) error {
	stable, err := s.ResolveAPIKeyStableID(publicID)
	if err != nil {
		return err
	}
	return s.adminUserTx(proof.ID, func(tx *sql.Tx) error {
		query := "DELETE FROM api_keys WHERE stable_id=?"
		args := []any{stable}
		if expectedVersion > 0 {
			query += " AND version=?"
			args = append(args, expectedVersion)
		}
		result, err := tx.Exec(query, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			if expectedVersion > 0 {
				return ErrAPIKeyVersionStale
			}
			return ErrAPIKeyNotFound
		}
		return nil
	}, proof)
}

func (s *Store) AdminRevokeAllAccessTokens(proof User) (int64, error) {
	var count int64
	err := s.adminUserTx(proof.ID, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE users SET auth_generation=auth_generation+1"); err != nil {
			return err
		}
		result, err := tx.Exec("DELETE FROM access_tokens")
		if err != nil {
			return err
		}
		count, err = result.RowsAffected()
		return err
	}, proof)
	return count, err
}
