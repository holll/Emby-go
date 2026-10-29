package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// R5：API 密钥增加稳定记录 ID、版本与轮转时间。schema 15 之前记录以摘要
// 作为 id；升级后 id=stable_id，摘要只作鉴权字段，绝不作为记录身份回显。
const apiKeyStableMigrationVersion = 15

var migration15Statements = []string{
	`ALTER TABLE api_keys ADD COLUMN stable_id TEXT`,
	`ALTER TABLE api_keys ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE api_keys ADD COLUMN rotated_at TEXT`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_stable_id ON api_keys(stable_id)`,
}

var (
	ErrAPIKeyNotFound       = errors.New("api key not found")
	ErrAPIKeyVersionStale   = errors.New("api key version conflict")
	ErrAPIKeyPrecondition   = errors.New("api key precondition required")
	ErrAdminGenerationStale = errors.New("administrator authentication changed")
)

func migration15Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration15Statements, "\n"))))
}

func applyMigration15(tx *sql.Tx) error {
	for _, statement := range migration15Statements[:3] {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
	}
	// 每行独立随机回填；先回填再建唯一索引，空值不参与唯一约束，但这里不留空。
	rows, err := tx.Query("SELECT key FROM api_keys WHERE stable_id IS NULL OR stable_id=''")
	if err != nil {
		return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
	}
	var legacy []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
		legacy = append(legacy, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
	}
	for _, key := range legacy {
		stable, err := newAPIKeyStableID()
		if err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
		if _, err := tx.Exec("UPDATE api_keys SET stable_id=? WHERE key=?", stable, key); err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
	}
	// 极小概率随机碰撞：保留一行，其余重新生成。
	for {
		var duplicate string
		err := tx.QueryRow("SELECT stable_id FROM api_keys GROUP BY stable_id HAVING COUNT(*)>1 LIMIT 1").Scan(&duplicate)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
		var firstKey string
		if err := tx.QueryRow("SELECT key FROM api_keys WHERE stable_id=? ORDER BY key LIMIT 1", duplicate).Scan(&firstKey); err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
		stable, err := newAPIKeyStableID()
		if err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
		if _, err := tx.Exec("UPDATE api_keys SET stable_id=? WHERE stable_id=? AND key<>?", stable, duplicate, firstKey); err != nil {
			return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
		}
	}
	if _, err := tx.Exec(migration15Statements[3]); err != nil {
		return fmt.Errorf("schema migration %d: %w", apiKeyStableMigrationVersion, err)
	}
	return nil
}

// newAPIKeyStableID 返回 16 字节随机值的 32 位小写十六进制。
func newAPIKeyStableID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// APIKeyPublicID 把内部 stable_id 转成对外 id（ak_ 前缀），便于与摘要区分。
func APIKeyPublicID(stableID string) string { return "ak_" + stableID }

// parseAPIKeyPublicID 接受 ak_<stable> 或裸 stable id；其他形式原样返回。
func parseAPIKeyPublicID(id string) string {
	id = strings.TrimSpace(id)
	if after, ok := strings.CutPrefix(id, "ak_"); ok {
		return after
	}
	return id
}

// ResolveAPIKeyStableID 把接口参数解析为内部 stable_id：优先稳定 ID，兼容
// 升级前页面仍在用的摘要 ID 与旧明文（只用于定位当前行，不作为身份回显）。
func (s *Store) ResolveAPIKeyStableID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", ErrAPIKeyNotFound
	}
	candidates := []string{parseAPIKeyPublicID(id)}
	if id != candidates[0] {
		candidates = append(candidates, id)
	}
	for _, candidate := range candidates {
		var stable string
		err := s.db.QueryRow("SELECT COALESCE(stable_id,'') FROM api_keys WHERE stable_id=? AND stable_id<>'' LIMIT 1", candidate).Scan(&stable)
		if err == nil && stable != "" {
			return stable, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	// 兼容摘要 ID（64 位 hex）与旧明文：只按鉴权字段定位，不再把摘要当身份。
	var rowKey, stable string
	err := s.db.QueryRow("SELECT key,COALESCE(stable_id,'') FROM api_keys WHERE key_hash=? OR key=? LIMIT 1", apiKeyHash(id), id).Scan(&rowKey, &stable)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAPIKeyNotFound
	}
	if err != nil {
		return "", err
	}
	if stable != "" {
		return stable, nil
	}
	// 升级后被直接 INSERT 的旧格式行（测试/外部工具）没有 stable_id：补一
	// 个稳定 ID，避免删除/轮转在空身份上静默失败。
	return s.backfillAPIKeyStableID(rowKey)
}

// apiKeyBackfillWaitBudget bounds how long the legacy stable_id backfill may
// queue for the writer lock. The backfill is a request-path write, so it must
// not inherit SQLite's 10s busy_timeout: 2s is short enough that a rotating
// administrator gets a prompt (retryable) failure instead of a request parked
// behind a long scan commit, and still long enough to absorb a normal commit.
const apiKeyBackfillWaitBudget = 2 * time.Second

// backfillAPIKeyStableID gives an old-format api_keys row (INSERTed without
// stable_id by pre-upgrade code or external tooling) a stable identity.
//
// P2-11: this used to be a bare s.db.Exec. That write bypassed s.writeMu, so it
// broke the "one in-process writer" invariant: racing a scan commit it either
// died with SQLITE_BUSY after busy_timeout (a 500 "database is locked" on an
// authentication/rotation path) or made the other writer's COMMIT fail. It now
// runs inside s.writeTx, which is the only path that owns both the writer gate
// and a pinned connection.
//
// Write frequency (why this is not a hot-path write): every INSERT this program
// makes already sets stable_id (store.go CreateAPIKeyForUser,
// admin_credentials.go AdminCreateAPIKey) and migration 15 backfilled all
// pre-existing rows, so only rows written by external tools reach here, once
// each — the UPDATE's guard makes that explicit, and the re-read inside the
// transaction means concurrent resolvers of the same row converge on one ID
// instead of overwriting each other.
//
// Worst-case added latency on the request path: apiKeyBackfillWaitBudget (2s)
// while another writer holds the gate, and only for those legacy rows; modern
// rows resolve entirely on the read path above and never touch this function.
func (s *Store) backfillAPIKeyStableID(rowKey string) (string, error) {
	candidate, err := newAPIKeyStableID()
	if err != nil {
		return "", err
	}
	var stable string
	// writeTxContext (not writeTx) so the wait is bounded: the callback may be
	// re-run on SQLITE_BUSY, hence it re-reads the row every attempt and never
	// writes a value it did not just verify was missing.
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyBackfillWaitBudget)
	defer cancel()
	err = s.writeTxContext(ctx, func(tx *sql.Tx) error {
		stable = ""
		err := tx.QueryRow("SELECT COALESCE(stable_id,'') FROM api_keys WHERE key=?", rowKey).Scan(&stable)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAPIKeyNotFound
		}
		if err != nil {
			return err
		}
		if stable != "" {
			return nil
		}
		if _, err := tx.Exec("UPDATE api_keys SET stable_id=? WHERE key=? AND (stable_id IS NULL OR stable_id='')", candidate, rowKey); err != nil {
			return err
		}
		// 以事务内读回的值为准：UPDATE 带了"只补空值"的守卫，外部工具可能已经在我们
		// 读完那一行之后写过 stable_id，这时不能把没落库的候选 ID 当成身份返回。
		return tx.QueryRow("SELECT COALESCE(stable_id,'') FROM api_keys WHERE key=?", rowKey).Scan(&stable)
	})
	if err != nil {
		return "", fmt.Errorf("backfill api key stable id: %w", err)
	}
	if stable == "" {
		// 行在事务内被并发删除（或从未有 stable_id 可回填）。
		return "", ErrAPIKeyNotFound
	}
	return stable, nil
}

// RotateAPIKey 在同一事务内替换摘要并递增版本。actor 必须来自刚刚的密码
// 再认证，其 auth_generation 用于阻止再认证后账户被改密/撤权仍提交。
func (s *Store) RotateAPIKey(actor User, publicID string, expectedVersion int64) (APIKey, error) {
	return s.rotateAPIKey(actor, publicID, expectedVersion, nil)
}

// RotateAPIKeyForRequest binds password reauthentication to the still-live
// interactive session in the same writer transaction as credential replacement.
func (s *Store) RotateAPIKeyForRequest(actor, proof User, publicID string, expectedVersion int64) (APIKey, error) {
	if actor.ID != proof.ID || !proof.requestSession {
		return APIKey{}, ErrAdminGenerationStale
	}
	return s.rotateAPIKey(actor, publicID, expectedVersion, &proof)
}

func (s *Store) rotateAPIKey(actor User, publicID string, expectedVersion int64, proof *User) (APIKey, error) {
	if actor.ID <= 0 || actor.authGeneration <= 0 || !actor.IsAdmin {
		return APIKey{}, ErrAdminGenerationStale
	}
	stable, err := s.ResolveAPIKeyStableID(publicID)
	if err != nil {
		return APIKey{}, err
	}
	plaintext, digest, prefix, err := newAPIKeyCredential()
	if err != nil {
		return APIKey{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var key APIKey
	write := func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE api_keys SET key=?,key_hash=?,key_prefix=?,version=version+1,rotated_at=?
   WHERE stable_id=? AND version=? AND EXISTS (
    SELECT 1 FROM users WHERE users.id=? AND users.auth_generation=? AND users.is_admin=1
   )`, digest, digest, prefix, now, stable, expectedVersion, actor.ID, actor.authGeneration)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return s.classifyAPIKeyConflict(tx, stable, expectedVersion, actor)
		}
		return tx.QueryRow(`SELECT COALESCE(stable_id,''),COALESCE(key_prefix,''),name,created_at,version,COALESCE(rotated_at,'') FROM api_keys WHERE stable_id=?`, stable).Scan(&key.ID, &key.KeyPrefix, &key.Name, &key.CreatedAt, &key.Version, &key.RotatedAt)
	}
	if proof != nil {
		err = s.adminUserTx(actor.ID, write, *proof)
	} else {
		err = s.writeTx(write)
	}
	if err != nil {
		return APIKey{}, err
	}
	key.ID = APIKeyPublicID(stable)
	key.Key = plaintext
	return key, nil
}

func (s *Store) classifyAPIKeyConflict(tx *sql.Tx, stable string, expectedVersion int64, actor User) error {
	var version int64
	var userID int64
	err := tx.QueryRow("SELECT version,COALESCE(user_id,0) FROM api_keys WHERE stable_id=?", stable).Scan(&version, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAPIKeyNotFound
	}
	if err != nil {
		return err
	}
	var generation int64
	var admin int
	err = tx.QueryRow("SELECT COALESCE(auth_generation,0),COALESCE(is_admin,0) FROM users WHERE id=?", actor.ID).Scan(&generation, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminGenerationStale
	}
	if err != nil {
		return err
	}
	if generation != actor.authGeneration || admin == 0 {
		return ErrAdminGenerationStale
	}
	if version != expectedVersion {
		return ErrAPIKeyVersionStale
	}
	// 版本号一致却未更新，只可能是 owner/约束变化；按版本冲突处理，不泄露细节。
	return ErrAPIKeyVersionStale
}

// DeleteAPIKeyByVersion 删除稳定 ID 对应记录；expectedVersion 为 0 表示不带
// If-Match 的旧式删除，>0 时要求版本匹配（删除与轮转以数据库写顺序裁决）。
func (s *Store) DeleteAPIKeyByVersion(publicID string, expectedVersion int64) error {
	stable, err := s.ResolveAPIKeyStableID(publicID)
	if err != nil {
		return err
	}
	query := "DELETE FROM api_keys WHERE stable_id=?"
	args := []any{stable}
	if expectedVersion > 0 {
		query += " AND version=?"
		args = append(args, expectedVersion)
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		if expectedVersion > 0 {
			return ErrAPIKeyVersionStale
		}
		return ErrAPIKeyNotFound
	}
	return nil
}

func newAPIKeyCredential() (plaintext, digest, prefix string, err error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", err
	}
	plaintext = hex.EncodeToString(buf)
	digest = apiKeyHash(plaintext)
	prefix = plaintext
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return plaintext, digest, prefix, nil
}
