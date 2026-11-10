package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User 是登录账户；id=1 为初始管理员，保持与旧单管理员数据兼容。
type User struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt string `json:"created_at"`
	// Captured by password authentication, never populated from JSON.
	authGeneration    int64
	requestCredential string
	requestSession    bool
}

var (
	ErrUserExists            = errors.New("用户名已存在")
	ErrUserNotFound          = errors.New("用户不存在")
	ErrLastAdmin             = errors.New("至少需要保留一个管理员")
	ErrProtectedUser         = errors.New("初始管理员不可删除")
	ErrInvalidUserName       = errors.New("用户名无效")
	ErrAuthenticationChanged = errors.New("authentication changed; sign in again")
)

const maxUserNameRunes = 64

func normalizeUserName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || len([]rune(n)) > maxUserNameRunes {
		return "", ErrInvalidUserName
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidUserName
		}
	}
	return n, nil
}

func userRowScanner(row interface{ Scan(...any) error }) (User, string, error) {
	var u User
	var admin int
	var hash string
	if err := row.Scan(&u.ID, &u.Name, &admin, &u.CreatedAt, &hash); err != nil {
		return User{}, "", err
	}
	u.IsAdmin = admin != 0
	return u, hash, nil
}

// ListUsers 返回全部账户（不含密码摘要），按 id 升序。
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query("SELECT id,name,is_admin,created_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var admin int
		if err := rows.Scan(&u.ID, &u.Name, &admin, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.IsAdmin = admin != 0
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UserByID(id int64) (User, error) {
	u, _, err := userRowScanner(s.db.QueryRow("SELECT id,name,is_admin,created_at,password_hash FROM users WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return u, err
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// AuthenticateUser 校验用户名密码，返回账户；未知用户也执行一次 bcrypt 以减小枚举时序差。
func (s *Store) AuthenticateUser(name, password string) (User, bool, error) {
	var u User
	var hash string
	var admin int
	err := s.db.QueryRow("SELECT id,name,is_admin,created_at,password_hash,auth_generation FROM users WHERE name=?", strings.TrimSpace(name)).Scan(&u.ID, &u.Name, &admin, &u.CreatedAt, &hash, &u.authGeneration)
	u.IsAdmin = admin != 0
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO4xY8hK6t7hYh8oQ0s1j2Qx7J0mHqXyK"), []byte(password))
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, false, nil
	}
	return u, true, nil
}

// SetAuthenticatedUserPassword prevents a stale old-password check from
// overwriting a concurrent password reset.
func (s *Store) SetAuthenticatedUserPassword(user User, password string) error {
	id, generation := user.ID, user.authGeneration
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	return s.writeTx(func(tx *sql.Tx) error {
		result, err := tx.Exec("UPDATE users SET password_hash=?, auth_generation=auth_generation+1, updated_at=? WHERE id=? AND auth_generation=?", hash, time.Now().UTC().Format(time.RFC3339), id, generation)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrAuthenticationChanged
		}
		_, err = tx.Exec("DELETE FROM access_tokens WHERE user_id=?", id)
		return err
	})
}

// An UPDATE as the first statement reserves SQLite's single writer before any
// role reads, preventing both check/write races and stale snapshot upgrades.
func lockUserTx(tx *sql.Tx, id int64) (bool, error) {
	var current int
	err := tx.QueryRow("UPDATE users SET is_admin=is_admin WHERE id=? RETURNING is_admin", id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrUserNotFound
	}
	return current != 0, err
}

func ensureAnotherAdminTx(tx *sql.Tx, id int64) error {
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin=1 AND id<>?", id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

// SaveAccessTokenForAuthenticatedUser atomically rejects authentications made
// stale by a password change, revocation or deletion, including in-flight bcrypt.
func (s *Store) SaveAccessTokenForAuthenticatedUser(token string, user User, expiresAt time.Time) error {
	userID, generation := user.ID, user.authGeneration
	var expiry any
	if !expiresAt.IsZero() {
		expiry = expiresAt.Unix()
	}
	query := `INSERT INTO access_tokens(token,created_at,expires_at,user_id,auth_generation)
	 SELECT ?,?,?,id,auth_generation FROM users WHERE id=? AND auth_generation=?`
	args := []any{tokenHash(token), time.Now().UTC().Format(time.RFC3339), expiry, userID, generation}
	// Duplicate tokens are not reassigned to another account.
	query += " ON CONFLICT(token) DO NOTHING"
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAuthenticationChanged
	}
	return nil
}

// ValidateAccessTokenUser validates a session and returns its bound account ID.
func (s *Store) ValidateAccessTokenUser(token string, now time.Time, ttl time.Duration) (int64, bool, error) {
	var expires sql.NullInt64
	var userID int64
	digest := tokenHash(token)
	if err := s.db.QueryRow("SELECT t.expires_at,t.user_id FROM access_tokens t JOIN users u ON u.id=t.user_id AND u.auth_generation=t.auth_generation WHERE t.token=?", digest).Scan(&expires, &userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !expires.Valid {
		return userID, true, nil
	}
	expiry := time.Unix(expires.Int64, 0)
	if !now.Before(expiry) {
		if _, err := s.db.Exec("DELETE FROM access_tokens WHERE token=?", digest); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if ttl > 0 {
		threshold := max(ttl-24*time.Hour, 0)
		if expiry.Sub(now) < threshold {
			_, _ = s.db.Exec("UPDATE access_tokens SET expires_at=? WHERE token=?", now.Add(ttl).Unix(), digest)
		}
	}
	return userID, true, nil
}

// APIKeyUser 返回 API 密钥所属账户（旧行默认 1 = 管理员）。
func (s *Store) APIKeyUser(key string) (int64, bool, error) {
	var userID int64
	err := s.db.QueryRow("SELECT user_id FROM api_keys WHERE key=?", apiKeyHash(key)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return userID, true, nil
}

// UserLastLogin 返回每个账户最近一次成功登录时间。
func (s *Store) UserLastLogin() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT user_id, MAX(created_at) FROM login_events WHERE success=1 AND user_id IS NOT NULL GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// RequestCredentialUser captures identity, generation and credential in one read.
// Only this opaque store-produced snapshot is accepted on request-bound writes.
func (s *Store) RequestCredentialUser(token string, userID int64, session bool) (User, error) {
	var u User
	var admin int
	digest := apiKeyHash(token)
	query := `SELECT u.id,u.name,u.is_admin,u.created_at,u.auth_generation FROM users u JOIN api_keys k ON k.user_id=u.id WHERE k.key=? AND u.id=?`
	args := []any{digest, userID}
	if session {
		digest = tokenHash(token)
		query = `SELECT u.id,u.name,u.is_admin,u.created_at,u.auth_generation FROM users u JOIN access_tokens t ON t.user_id=u.id AND t.auth_generation=u.auth_generation WHERE t.token=? AND u.id=? AND (t.expires_at IS NULL OR t.expires_at>?)`
		args = []any{digest, userID, time.Now().Unix()}
	}
	err := s.db.QueryRow(query, args...).Scan(&u.ID, &u.Name, &admin, &u.CreatedAt, &u.authGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.IsAdmin = admin != 0
	u.requestCredential, u.requestSession = digest, session
	return u, nil
}
