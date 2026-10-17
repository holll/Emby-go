package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Migration 1 adopts the schema previously created by Open. It does not delete
// historical rows: destructive data cleanup needs a separate, approved migration.
// Keep these definitions immutable after release; append a new migration instead.
var baselineColumns = []struct{ table, name, definition string }{
	{"userdata", "last_stopped_ticks", "INTEGER DEFAULT -1"},
	{"userdata", "is_favorite", "INTEGER NOT NULL DEFAULT 0"},
	{"userdata", "likes", "INTEGER NOT NULL DEFAULT 0"},
	{"userdata", "hide_from_resume", "INTEGER NOT NULL DEFAULT 0"},
	{"movies", "landscape_path", "TEXT"},
	{"movies", "collection", "TEXT"},
	{"movies", "official_rating", "TEXT"},
	{"movies", "sortname", "TEXT"},
	{"movies", "taglines", "TEXT"},
	{"movies", "provider_id", "TEXT"},
	{"movies", "additional_parts", "TEXT NOT NULL DEFAULT '[]'"},
	{"actors", "avatar_tag", "TEXT"},
	{"movies", "last_scrape_at", "TEXT"},
	{"movies", "last_scrape_error", "TEXT"},
	{"libraries", "sort_order", "INTEGER NOT NULL DEFAULT 0"},
}

var baselineIndexes = []string{
	"CREATE INDEX IF NOT EXISTS idx_movies_library_status ON movies(library_id,status)",
	"CREATE INDEX IF NOT EXISTS idx_movies_status ON movies(status)",
	"CREATE INDEX IF NOT EXISTS idx_movies_collection ON movies(collection)",
	"CREATE INDEX IF NOT EXISTS idx_movie_features_lookup ON movie_features(kind,value,movie_id)",
}

const migrationLedgerSchema = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL
)`

var baselineTables = []string{
	"libraries", "movies", "userdata", "api_probe", "actors", "settings",
	"movie_actors", "movie_features", "kv", "administrators", "access_tokens",
	"api_keys", "scheduled_tasks",
}

type schemaDB interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}

type schemaColumn struct {
	Name, Type          string
	NotNull, PrimaryKey int
	Hidden              int
	Default             sql.NullString
}

func tableColumns(db schemaDB, table string) (map[string]schemaColumn, error) {
	// table names come only from the frozen definitions above, never input.
	rows, err := db.Query("PRAGMA table_xinfo(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]schemaColumn)
	for rows.Next() {
		var cid int
		var c schemaColumn
		if err := rows.Scan(&cid, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.PrimaryKey, &c.Hidden); err != nil {
			return nil, err
		}
		c.Type = strings.ToUpper(c.Type)
		columns[c.Name] = c
	}
	return columns, rows.Err()
}

func applyBaseline(db schemaDB) error {
	if _, err := db.Exec(baseSchema); err != nil {
		return fmt.Errorf("create base tables: %w", err)
	}
	for _, c := range baselineColumns {
		columns, err := tableColumns(db, c.table)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", c.table, err)
		}
		if _, exists := columns[c.name]; exists {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.name + " " + c.definition); err != nil {
			return fmt.Errorf("add %s.%s: %w", c.table, c.name, err)
		}
	}
	for _, statement := range baselineIndexes {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("create baseline index: %w", err)
		}
	}
	return nil
}

func baselineChecksum() string {
	var b strings.Builder
	b.WriteString(baseSchema)
	for _, c := range baselineColumns {
		fmt.Fprintf(&b, "\n%s.%s %s", c.table, c.name, c.definition)
	}
	for _, statement := range baselineIndexes {
		b.WriteString("\n" + statement)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

// Validate against an isolated canonical schema instead of maintaining a second
// hand-written list of expected columns. Column order may differ in legacy DBs.
// Unknown/malformed structures fail closed rather than earning a migration stamp.
// migrationAddedColumns 是后续迁移在 baseline 表上新增的列：校验只放行这些已知定义，
// 其它未备案列仍然按漂移失败处理。
var migrationAddedColumns = map[string]map[string]string{
	"access_tokens": {"expires_at": "INTEGER"},
}

func validateBaseline(db schemaDB) error {
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	reference.SetMaxOpenConns(1)
	if err := applyBaseline(reference); err != nil {
		return fmt.Errorf("build reference schema: %w", err)
	}
	if _, err := reference.Exec(migrationLedgerSchema); err != nil {
		return err
	}
	tables := append(append([]string(nil), baselineTables...), "schema_migrations")
	for _, table := range tables {
		want, err := tableColumns(reference, table)
		if err != nil {
			return err
		}
		got, err := tableColumns(db, table)
		if err != nil {
			return err
		}
		for name, wantType := range migrationAddedColumns[table] {
			column, ok := got[name]
			if !ok || column.Type != wantType {
				return fmt.Errorf("schema drift in table %s (expected migrated column %s %s)", table, name, wantType)
			}
			delete(got, name)
		}
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("schema drift in table %s (columns, types, defaults or constraints)", table)
		}
		// Foreign key definitions are ordered by declaration, unlike column IDs.
		wantFK, err := foreignKeys(reference, table)
		if err != nil {
			return err
		}
		gotFK, err := foreignKeys(db, table)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(gotFK, wantFK) {
			return fmt.Errorf("schema drift in table %s (foreign keys)", table)
		}
		wantUnique, err := uniqueConstraints(reference, table)
		if err != nil {
			return err
		}
		gotUnique, err := uniqueConstraints(db, table)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(gotUnique, wantUnique) {
			return fmt.Errorf("schema drift in table %s (unique constraints)", table)
		}
		wantChecks, err := checkConstraints(reference, table)
		if err != nil {
			return err
		}
		gotChecks, err := checkConstraints(db, table)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(gotChecks, wantChecks) {
			return fmt.Errorf("schema drift in table %s (check constraints)", table)
		}
	}
	for _, statement := range baselineIndexes {
		name := strings.Fields(statement)[5]
		want, err := indexSignature(reference, name)
		if err != nil {
			return err
		}
		got, err := indexSignature(db, name)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("schema drift in index %s", name)
		}
	}
	return nil
}

func foreignKeys(db schemaDB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id, seq int
		var target, from, to, update, delete, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &update, &delete, &match); err != nil {
			return nil, err
		}
		result = append(result, fmt.Sprintf("%d/%d/%s/%s/%s/%s/%s/%s", id, seq, target, from, to, update, delete, match))
	}
	return result, rows.Err()
}

// indexSignature includes key order, collation, direction, uniqueness and
// partial status. Baseline indexes contain no expressions or partial predicates;
// either extension is rejected instead of being mistaken for a full index.
func indexSignature(db schemaDB, name string) (string, error) {
	rows, err := db.Query("SELECT tbl_name FROM sqlite_master WHERE type='index' AND name=?", name)
	if err != nil {
		return "", err
	}
	var table string
	if rows.Next() {
		err = rows.Scan(&table)
	} else {
		err = fmt.Errorf("schema drift: missing index %s", name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", err
	}
	indexes, err := tableIndexes(db, table)
	if err != nil {
		return "", err
	}
	flags, ok := indexes[name]
	if !ok {
		return "", fmt.Errorf("schema drift: missing index %s", name)
	}
	if flags.partial != 0 {
		return "", fmt.Errorf("schema drift: unexpected partial index %s", name)
	}
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	rows, err = db.Query("PRAGMA index_xinfo(" + quote(name) + ")")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "%s/unique=%d", table, flags.unique)
	for rows.Next() {
		var seq, cid, desc, key int
		var column, collation sql.NullString
		if err := rows.Scan(&seq, &cid, &column, &desc, &collation, &key); err != nil {
			return "", err
		}
		if key == 0 {
			continue
		}
		if cid < 0 || !column.Valid {
			return "", fmt.Errorf("schema drift: unexpected expression index %s", name)
		}
		fmt.Fprintf(&b, "/%s:%s:%d", column.String, strings.ToUpper(collation.String), desc)
	}
	return b.String(), rows.Err()
}

type indexFlags struct{ unique, partial int }

func tableIndexes(db schemaDB, table string) (map[string]indexFlags, error) {
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := db.Query("PRAGMA index_list(" + quoted + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]indexFlags)
	for rows.Next() {
		var seq int
		var name, origin string
		var flags indexFlags
		if err := rows.Scan(&seq, &name, &flags.unique, &origin, &flags.partial); err != nil {
			return nil, err
		}
		result[name] = flags
	}
	return result, rows.Err()
}

func uniqueConstraints(db schemaDB, table string) ([]string, error) {
	indexes, err := tableIndexes(db, table)
	if err != nil {
		return nil, err
	}
	var result []string
	for name, flags := range indexes {
		if flags.unique == 0 {
			continue
		}
		signature, err := indexSignature(db, name)
		if err != nil {
			return nil, err
		}
		result = append(result, signature)
	}
	sort.Strings(result)
	return result, nil
}

// SQLite exposes no CHECK/AUTOINCREMENT pragma. Tokenize DDL to compare these
// properties without treating comments or string literals as SQL keywords.
func schemaTokens(s string) []string {
	var tokens []string
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			i++
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "--" {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i:i+2] == "/*" {
			i += 2
			for i+1 < len(s) && s[i:i+2] != "*/" {
				i++
			}
			i += 2
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			start := i
			i++
			end := c
			if c == '[' {
				end = ']'
			}
			for i < len(s) {
				if s[i] == end {
					i++
					if i < len(s) && s[i] == end && c != '[' {
						i++
						continue
					}
					break
				}
				i++
			}
			tokens = append(tokens, s[start:i])
			continue
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			start := i
			i++
			for i < len(s) {
				c = s[i]
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
					break
				}
				i++
			}
			tokens = append(tokens, strings.ToLower(s[start:i]))
			continue
		}
		tokens = append(tokens, string(c))
		i++
	}
	return tokens
}

func checkConstraints(db schemaDB, table string) ([]string, error) {
	rows, err := db.Query("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table)
	if err != nil {
		return nil, err
	}
	var ddl string
	if rows.Next() {
		err = rows.Scan(&ddl)
	} else {
		err = fmt.Errorf("schema drift: missing table %s", table)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	tokens := schemaTokens(ddl)
	var result []string
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "autoincrement", "strict":
			result = append(result, tokens[i])
		case "without":
			if i+1 < len(tokens) && tokens[i+1] == "rowid" {
				result = append(result, "without rowid")
			}
		case "check":
			if i+1 >= len(tokens) || tokens[i+1] != "(" {
				return nil, fmt.Errorf("schema drift: malformed CHECK in %s", table)
			}
			start := i
			i++
			depth := 0
			for ; i < len(tokens); i++ {
				if tokens[i] == "(" {
					depth++
				}
				if tokens[i] == ")" {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if depth != 0 {
				return nil, fmt.Errorf("schema drift: malformed CHECK in %s", table)
			}
			result = append(result, strings.Join(tokens[start:i+1], " "))
		}
	}
	sort.Strings(result)
	return result, nil
}

// Migration 2（D07 已批）：播放会话/事件表，承载「一次播放只计一次」的
// 序列号协议。sessions 以客户端 PlaySessionId（缺失时回退 item:<id>）为主键，
// last_event_seq 单调递增；stopped 幂等由「仅在 stopped=0 时迁移」保证。
const playbackMigrationVersion = 2

var migration2Statements = []string{
	`CREATE TABLE IF NOT EXISTS playback_sessions (
		session_key TEXT PRIMARY KEY,
		movie_id INTEGER NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
		device_id TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		last_event_seq INTEGER NOT NULL DEFAULT 0,
		position_ticks INTEGER NOT NULL DEFAULT 0,
		stopped INTEGER NOT NULL DEFAULT 0,
		stopped_at TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS playback_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key TEXT NOT NULL REFERENCES playback_sessions(session_key) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		position_ticks INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		UNIQUE(session_key, seq)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_playback_sessions_movie ON playback_sessions(movie_id)`,
	`CREATE INDEX IF NOT EXISTS idx_playback_events_session ON playback_events(session_key, seq)`,
}

func migration2Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration2Statements, "\n"))))
}

func applyMigration2(tx *sql.Tx) error {
	for _, statement := range migration2Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration 2: %w", err)
		}
	}
	return nil
}

const sessionExpiryMigrationVersion = 3

// migration3Statements 给登录会话加过期时间（unix 秒，NULL=不过期）。
// 旧行按 created_at + 30 天回填；created_at 无法解析时保持 NULL（不过期），
// 避免把未知历史数据误判为已过期。
var migration3Statements = []string{
	`ALTER TABLE access_tokens ADD COLUMN expires_at INTEGER`,
	`UPDATE access_tokens SET expires_at = CAST(strftime('%s', created_at) AS INTEGER) + 2592000 WHERE expires_at IS NULL`,
}

func migration3Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(migration3Statements, "\n"))))
}

func applyMigration3(tx *sql.Tx) error {
	for _, statement := range migration3Statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("schema migration 3: %w", err)
		}
	}
	return nil
}

func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrationLedgerSchema); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	rows, err := tx.Query("SELECT version,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	applied := map[int]bool{}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		switch version {
		case 1:
			if checksum != baselineChecksum() {
				rows.Close()
				return fmt.Errorf("unsupported or modified schema migration %d", version)
			}
		case playbackMigrationVersion:
			if checksum != migration2Checksum() {
				rows.Close()
				return fmt.Errorf("unsupported or modified schema migration %d", version)
			}
		case sessionExpiryMigrationVersion:
			if checksum != migration3Checksum() {
				rows.Close()
				return fmt.Errorf("unsupported or modified schema migration %d", version)
			}
		default:
			rows.Close()
			return fmt.Errorf("unsupported or modified schema migration %d", version)
		}
		applied[version] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !applied[1] {
		if err := applyBaseline(tx); err != nil {
			return fmt.Errorf("schema migration 1: %w", err)
		}
	}
	if !applied[playbackMigrationVersion] {
		if err := applyMigration2(tx); err != nil {
			return err
		}
	}
	if !applied[sessionExpiryMigrationVersion] {
		if err := applyMigration3(tx); err != nil {
			return err
		}
	}
	if err := validateBaseline(tx); err != nil {
		return fmt.Errorf("validate schema migration 1: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if !applied[1] {
		if _, err := tx.Exec("INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(1,?,?)", baselineChecksum(), now); err != nil {
			return fmt.Errorf("record schema migration 1: %w", err)
		}
	}
	if !applied[playbackMigrationVersion] {
		if _, err := tx.Exec("INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)", playbackMigrationVersion, migration2Checksum(), now); err != nil {
			return fmt.Errorf("record schema migration %d: %w", playbackMigrationVersion, err)
		}
	}
	if !applied[sessionExpiryMigrationVersion] {
		if _, err := tx.Exec("INSERT INTO schema_migrations(version,checksum,applied_at) VALUES(?,?,?)", sessionExpiryMigrationVersion, migration3Checksum(), now); err != nil {
			return fmt.Errorf("record schema migration %d: %w", sessionExpiryMigrationVersion, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}
