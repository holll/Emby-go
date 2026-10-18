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
}

var (
	ErrUserExists      = errors.New("用户名已存在")
	ErrUserNotFound    = errors.New("用户不存在")
	ErrLastAdmin       = errors.New("至少需要保留一个管理员")
	ErrProtectedUser   = errors.New("初始管理员不可删除")
	ErrInvalidUserName = errors.New("用户名无效")
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

// CreateUser 新建账户；密码使用 bcrypt 摘要，用户名唯一。
func (s *Store) CreateUser(name, password string, isAdmin bool) (User, error) {
	n, err := normalizeUserName(name)
	if err != nil {
		return User{}, err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	admin := 0
	if isAdmin {
		admin = 1
	}
	result, err := s.db.Exec("INSERT INTO users(name,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,?)", n, hash, admin, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrUserExists
		}
		return User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Name: n, IsAdmin: isAdmin, CreatedAt: now}, nil
}

// AuthenticateUser 校验用户名密码，返回账户；未知用户也执行一次 bcrypt 以减小枚举时序差。
func (s *Store) AuthenticateUser(name, password string) (User, bool, error) {
	u, hash, err := userRowScanner(s.db.QueryRow("SELECT id,name,is_admin,created_at,password_hash FROM users WHERE name=?", strings.TrimSpace(name)))
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

func (s *Store) SetUserPassword(id int64, password string) error {
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	result, err := s.db.Exec("UPDATE users SET password_hash=?, updated_at=? WHERE id=?", hash, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *Store) RenameUser(id int64, name string) error {
	n, err := normalizeUserName(name)
	if err != nil {
		return err
	}
	result, err := s.db.Exec("UPDATE users SET name=?, updated_at=? WHERE id=?", n, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrUserExists
		}
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *Store) SetUserAdmin(id int64, admin bool) error {
	var current int
	if err := s.db.QueryRow("SELECT is_admin FROM users WHERE id=?", id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if current != 0 && !admin {
		if err := s.ensureAnotherAdmin(id); err != nil {
			return err
		}
	}
	flag := 0
	if admin {
		flag = 1
	}
	_, err := s.db.Exec("UPDATE users SET is_admin=?, updated_at=? WHERE id=?", flag, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *Store) ensureAnotherAdmin(id int64) error {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin=1 AND id<>?", id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

// DeleteUser 删除账户及其会话/API 密钥；初始管理员与最后一名管理员不可删除。
func (s *Store) DeleteUser(id int64) error {
	if id == 1 {
		return ErrProtectedUser
	}
	u, err := s.UserByID(id)
	if err != nil {
		return err
	}
	if u.IsAdmin {
		if err := s.ensureAnotherAdmin(id); err != nil {
			return err
		}
	}
	return s.writeTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM access_tokens WHERE user_id=?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM api_keys WHERE user_id=?", id); err != nil {
			return err
		}
		// 播放/登录审计保留（含用户 id 快照）；个人播放数据随账户删除。
		if _, err := tx.Exec("DELETE FROM userdata WHERE user_id=?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM user_libraries WHERE user_id=?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM users WHERE id=?", id); err != nil {
			return err
		}
		return nil
	})
}

// SaveAccessTokenForUser 保存带账户绑定的登录会话；expiresAt 为零值时不过期。
func (s *Store) SaveAccessTokenForUser(token string, userID int64, expiresAt time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if expiresAt.IsZero() {
		_, err := s.db.Exec("INSERT OR IGNORE INTO access_tokens(token,created_at,expires_at,user_id) VALUES(?,?,NULL,?)", token, now, userID)
		return err
	}
	_, err := s.db.Exec("INSERT OR IGNORE INTO access_tokens(token,created_at,expires_at,user_id) VALUES(?,?,?,?)", token, now, expiresAt.Unix(), userID)
	return err
}

// ValidateAccessTokenUser 与 ValidateAccessToken 相同，但返回会话绑定的账户 id。
func (s *Store) ValidateAccessTokenUser(token string, now time.Time, ttl time.Duration) (int64, bool, error) {
	var expires sql.NullInt64
	var userID int64
	if err := s.db.QueryRow("SELECT expires_at,user_id FROM access_tokens WHERE token=?", token).Scan(&expires, &userID); err != nil {
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
		if _, err := s.db.Exec("DELETE FROM access_tokens WHERE token=?", token); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if ttl > 0 {
		threshold := ttl - 24*time.Hour
		if threshold < 0 {
			threshold = 0
		}
		if expiry.Sub(now) < threshold {
			_, _ = s.db.Exec("UPDATE access_tokens SET expires_at=? WHERE token=?", now.Add(ttl).Unix(), token)
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

// RevokeUserAccessTokens 撤销某账户的全部登录会话（改密后强制重新登录）。
func (s *Store) RevokeUserAccessTokens(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM access_tokens WHERE user_id=?`, userID)
	return err
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
