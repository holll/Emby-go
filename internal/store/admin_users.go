package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AdminUserUpdate is an explicit allowlist; nil fields remain unchanged.
type AdminUserUpdate struct {
	Name       *string
	Password   *string
	IsAdmin    *bool
	LibraryIDs *[]int64
}

// adminUserTx reserves SQLite's writer before checking the actor. Demotion or
// deletion committed before this transaction must win, including across Stores.
// The mandatory opaque credential proof binds the check to request authentication.
// Do not replace this with an HTTP-layer preflight check.
func (s *Store) adminUserTx(actorID int64, write func(*sql.Tx) error, proof User) error {
	return s.adminUserTxContext(context.Background(), actorID, write, proof)
}

// adminUserTxContext reserves SQLite's writer before checking the actor and
// abandons that wait when the caller's request context is cancelled. Admission
// must never outlive the request that asked for it while holding a lock.
func (s *Store) adminUserTxContext(ctx context.Context, actorID int64, write func(*sql.Tx) error, proof User) error {
	return s.writeTxContext(ctx, func(tx *sql.Tx) error {
		admin, err := lockUserTx(tx, actorID)
		if errors.Is(err, ErrUserNotFound) || (err == nil && !admin) {
			return ErrAdminGenerationStale
		}
		if err != nil {
			return err
		}
		{
			u := proof
			var generation int64
			if err := tx.QueryRow("SELECT auth_generation FROM users WHERE id=?", actorID).Scan(&generation); err != nil {
				return err
			}
			if u.ID != actorID || u.requestCredential == "" || u.authGeneration != generation {
				return ErrAdminGenerationStale
			}
			var exists int
			query := "SELECT 1 FROM api_keys WHERE key=? AND user_id=?"
			if u.requestSession {
				query = "SELECT 1 FROM access_tokens WHERE token=? AND user_id=? AND auth_generation=? AND (expires_at IS NULL OR expires_at>?)"
			}
			args := []any{u.requestCredential, actorID}
			if u.requestSession {
				args = append(args, generation, time.Now().Unix())
			}
			if err := tx.QueryRow(query, args...).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
				return ErrAdminGenerationStale
			} else if err != nil {
				return err
			}
		}
		return write(tx)
	})
}

func setAdminUserLibrariesTx(tx *sql.Tx, id int64, ids []int64) error {
	for _, libraryID := range ids {
		var exists int
		if err := tx.QueryRow("SELECT 1 FROM libraries WHERE id=?", libraryID).Scan(&exists); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM user_libraries WHERE user_id=?", id); err != nil {
		return err
	}
	for _, libraryID := range ids {
		if _, err := tx.Exec("INSERT OR IGNORE INTO user_libraries(user_id,library_id) VALUES(?,?)", id, libraryID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AdminCreateUser(actorID int64, name, password string, admin bool, libs []int64, proof User) (User, error) {
	n, err := normalizeUserName(name)
	if err != nil {
		return User{}, err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err = s.adminUserTx(actorID, func(tx *sql.Tx) error {
		result, err := tx.Exec("INSERT INTO users(name,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,?)", n, hash, admin, now, now)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return err
		}
		return setAdminUserLibrariesTx(tx, id, libs)
	}, proof)
	if err != nil {
		return User{}, adminUserError(err)
	}
	return User{ID: id, Name: n, IsAdmin: admin, CreatedAt: now}, nil
}

func adminUserError(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrUserExists
	}
	return err
}

// AdminUpdateUser commits the actor check and all requested fields atomically.
// Hashing happens before acquiring the writer; authority is checked afterwards.
func (s *Store) AdminUpdateUser(actorID, id int64, update AdminUserUpdate, proof User) error {
	var name, hash string
	var err error
	if update.Name != nil {
		name, err = normalizeUserName(*update.Name)
		if err != nil {
			return err
		}
	}
	if update.Password != nil {
		hash, err = passwordHash(*update.Password)
		if err != nil {
			return err
		}
	}
	err = s.adminUserTx(actorID, func(tx *sql.Tx) error {
		current, err := lockUserTx(tx, id)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if update.Name != nil {
			if _, err := tx.Exec("UPDATE users SET name=?,updated_at=? WHERE id=?", name, now, id); err != nil {
				return err
			}
		}
		if update.Password != nil {
			if _, err := tx.Exec("UPDATE users SET password_hash=?,auth_generation=auth_generation+1,updated_at=? WHERE id=?", hash, now, id); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM access_tokens WHERE user_id=?", id); err != nil {
				return err
			}
		}
		if update.IsAdmin != nil {
			if current && !*update.IsAdmin {
				if err := ensureAnotherAdminTx(tx, id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec("UPDATE users SET auth_generation=auth_generation+CASE WHEN is_admin<>? THEN 1 ELSE 0 END,is_admin=?,updated_at=? WHERE id=?", *update.IsAdmin, *update.IsAdmin, now, id); err != nil {
				return err
			}
		}
		if update.LibraryIDs != nil {
			return setAdminUserLibrariesTx(tx, id, *update.LibraryIDs)
		}
		return nil
	}, proof)
	return adminUserError(err)
}

func (s *Store) AdminDeleteUser(actorID, id int64, proof User) error {
	return s.adminUserTx(actorID, func(tx *sql.Tx) error {
		if id == 1 {
			return ErrProtectedUser
		}
		admin, err := lockUserTx(tx, id)
		if err != nil {
			return err
		}
		if admin {
			if err := ensureAnotherAdminTx(tx, id); err != nil {
				return err
			}
		}
		for _, table := range []string{"access_tokens", "api_keys", "userdata", "user_libraries"} {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE user_id=?", id); err != nil {
				return err
			}
		}
		_, err = tx.Exec("DELETE FROM users WHERE id=?", id)
		return err
	}, proof)
}
