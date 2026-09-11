package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// Store 的 gversion 是 kv 中 'g:version' 的进程内镜像：列表/详情缓存键都含该值，
// 每个请求都要读一次，走内存可免去单连接 SQLite 的串行查询。
type Store struct {
	db       *sql.DB
	gversion atomic.Int64
}

type Library struct {
	ID   int64  `json:"Id"`
	Name string `json:"Name"`
	Path string `json:"Path"`
}

type Movie struct {
	ID              int64  `json:"id"`
	LibraryID       int64  `json:"library_id"`
	SourcePath      string `json:"source_path"`
	SourceProtocol  string `json:"source_protocol"`
	SourceContainer string `json:"source_container"`
	Status          string
	NFOPath         string
	OutputDir       string
	Number          string
	Title           string
	OriginalTitle   string
	Plot            string
	Year            int
	Premiere        string
	Rating          float64
	Director        string
	Series          string
	Maker           string
	Label           string
	Collection      string `json:"collection"` // NFO <set><name>：所属合集（BoxSet）
	Genres          []string
	Tags            []string
	Studios         []string
	Taglines        []string
	OfficialRating  string `json:"official_rating"` // NFO <mpaa>
	SortName        string `json:"sortname"`        // NFO <sorttitle>
	ProviderID      string `json:"provider_id"`     // NFO uniqueid type=metatube
	PosterPath      string
	BackdropPath    string
	LandscapePath   string
	RuntimeSeconds  int64
	AdditionalParts []string
	// CreatedAt 首次入库时间（重扫不变），UpdatedAt 最近一次索引更新时间。
	// 对应 Emby 的 DateCreated / DateModified；存储为 RFC3339 字符串。
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// 刮削结果（需求 5f）：LastScrapeError 非空表示上次刮削失败或待人工确认
	//（待确认带 store.ScrapeConfirmPrefix 前缀）。不新增 status 枚举值。
	LastScrapeAt    string `json:"last_scrape_at,omitempty"`
	LastScrapeError string `json:"last_scrape_error,omitempty"`
}

type UserData struct {
	PositionTicks    int64   `json:"PlaybackPositionTicks"`
	PlayCount        int64   `json:"PlayCount"`
	Played           bool    `json:"Played"`
	PlayedPercentage float64 `json:"PlayedPercentage,omitempty"`
	LastPlayedAt     string  `json:"LastPlayedDate,omitempty"`
	IsFavorite       bool    `json:"IsFavorite"`
	StoppedTicks     int64   `json:"-"`
	Likes            int     `json:"-"` // 0 未评 / 1 赞 / -1 踩
	HideFromResume   bool    `json:"-"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	_, _ = db.Exec("ALTER TABLE userdata ADD COLUMN last_stopped_ticks INTEGER DEFAULT -1")
	_, _ = db.Exec("ALTER TABLE userdata ADD COLUMN is_favorite INTEGER NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE userdata ADD COLUMN likes INTEGER NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE userdata ADD COLUMN hide_from_resume INTEGER NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN landscape_path TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN collection TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN official_rating TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN sortname TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN taglines TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN provider_id TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN additional_parts TEXT NOT NULL DEFAULT '[]'")
	// 头像内容标识（Emby PrimaryImageTag）：存量库通过 ALTER 补列，新库见 init 的建表语句。
	_, _ = db.Exec("ALTER TABLE actors ADD COLUMN avatar_tag TEXT")
	// 刮削结果可见性（需求 5f）：不新增 status 枚举，失败/待确认都记在这两列上。
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN last_scrape_at TEXT")
	_, _ = db.Exec("ALTER TABLE movies ADD COLUMN last_scrape_error TEXT")
	// 列表/详情/续播/实体聚合都按 status（+ library_id/collection）过滤，建索引避免全表扫描。
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_movies_library_status ON movies(library_id,status)")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_movies_status ON movies(status)")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_movies_collection ON movies(collection)")
	// 载入全局版本号到内存（kv 无该行时视为 0）。
	var version string
	_ = db.QueryRow("SELECT value FROM kv WHERE key='g:version'").Scan(&version)
	if n, err := strconv.ParseInt(version, 10, 64); err == nil {
		s.gversion.Store(n)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	_, err := s.db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS libraries (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, path TEXT UNIQUE NOT NULL, type TEXT DEFAULT 'movies', enabled INTEGER DEFAULT 1);
CREATE TABLE IF NOT EXISTS movies (id INTEGER PRIMARY KEY AUTOINCREMENT, library_id INTEGER NOT NULL, source_path TEXT UNIQUE NOT NULL, file_size INTEGER DEFAULT 0, file_mtime TEXT, source_protocol TEXT, source_container TEXT, number TEXT, status TEXT NOT NULL, nfo_path TEXT, output_dir TEXT, title TEXT, original_title TEXT, plot TEXT, year INTEGER, premiered TEXT, rating REAL, director TEXT, series TEXT, maker TEXT, label TEXT, collection TEXT, official_rating TEXT, sortname TEXT, taglines TEXT, provider_id TEXT, genres TEXT, tags TEXT, studios TEXT, poster_path TEXT, backdrop_path TEXT, landscape_path TEXT, runtime_seconds INTEGER DEFAULT 0, additional_parts TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY(library_id) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS userdata (movie_id INTEGER PRIMARY KEY REFERENCES movies(id) ON DELETE CASCADE, position_ticks INTEGER DEFAULT 0, play_count INTEGER DEFAULT 0, played INTEGER DEFAULT 0, last_played_at TEXT, last_stopped_ticks INTEGER DEFAULT -1, is_favorite INTEGER NOT NULL DEFAULT 0, likes INTEGER NOT NULL DEFAULT 0, hide_from_resume INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS api_probe (id INTEGER PRIMARY KEY AUTOINCREMENT, method TEXT, path TEXT, query TEXT, body_preview TEXT, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS actors (name TEXT PRIMARY KEY, avatar_url TEXT, avatar_tag TEXT, updated_at TEXT);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS movie_actors (movie_id INTEGER, actor_name TEXT, PRIMARY KEY(movie_id, actor_name), FOREIGN KEY(movie_id) REFERENCES movies(id) ON DELETE CASCADE, FOREIGN KEY(actor_name) REFERENCES actors(name));
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '0');
CREATE TABLE IF NOT EXISTS administrators (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS access_tokens (token TEXT PRIMARY KEY, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS api_keys (key TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scheduled_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, type TEXT NOT NULL, cron TEXT NOT NULL, params TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT, last_status TEXT, last_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);`)
	return err
}

func passwordHash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func (s *Store) HasAdministrator() (bool, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM administrators WHERE id=1").Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *Store) InitializeAdministrator(username, password string) error {
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO administrators(id,username,password_hash,created_at) VALUES(1,?,?,?)", username, hash, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) AuthenticateAdministrator(username, password string) (bool, error) {
	var stored string
	if err := s.db.QueryRow("SELECT password_hash FROM administrators WHERE id=1 AND username=?", username).Scan(&stored); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil, nil
}

func (s *Store) AdministratorName() (string, error) {
	var username string
	err := s.db.QueryRow("SELECT username FROM administrators WHERE id=1").Scan(&username)
	return username, err
}

func (s *Store) SaveAccessToken(token string) error {
	_, err := s.db.Exec("INSERT OR IGNORE INTO access_tokens(token,created_at) VALUES(?,?)", token, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) HasAccessToken(token string) (bool, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM access_tokens WHERE token=?", token).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

// APIKey 长期凭据：与登录令牌一样可作 X-Emby-Token / api_key 调用 Emby API。
type APIKey struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) APIKeys() ([]APIKey, error) {
	rows, err := s.db.Query("SELECT key,name,created_at FROM api_keys ORDER BY created_at DESC, key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIKey, 0)
	for rows.Next() {
		var v APIKey
		if err := rows.Scan(&v.Key, &v.Name, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateAPIKey(name string) (APIKey, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return APIKey{}, err
	}
	v := APIKey{Key: hex.EncodeToString(buf), Name: strings.TrimSpace(name), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	_, err := s.db.Exec("INSERT INTO api_keys(key,name,created_at) VALUES(?,?,?)", v.Key, v.Name, v.CreatedAt)
	return v, err
}

func (s *Store) DeleteAPIKey(key string) error {
	result, err := s.db.Exec("DELETE FROM api_keys WHERE key=?", key)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) HasAPIKey(key string) (bool, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE key=?", key).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *Store) AddLibrary(name, path string) (Library, error) {
	res, err := s.db.Exec("INSERT INTO libraries(name,path) VALUES(?,?) ON CONFLICT(path) DO UPDATE SET name=excluded.name", name, path)
	if err != nil {
		return Library{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Library{}, err
	}
	if id == 0 {
		err = s.db.QueryRow("SELECT id FROM libraries WHERE path=?", path).Scan(&id)
		if err != nil {
			return Library{}, err
		}
	}
	return Library{ID: id, Name: name, Path: path}, nil
}

// Library 按 id 返回单个启用的媒体库。
func (s *Store) Library(id int64) (Library, error) {
	var v Library
	err := s.db.QueryRow("SELECT id,name,path FROM libraries WHERE id=? AND enabled=1", id).Scan(&v.ID, &v.Name, &v.Path)
	return v, err
}

func (s *Store) Libraries() ([]Library, error) {
	rows, err := s.db.Query("SELECT id,name,path FROM libraries WHERE enabled=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var v Library
		if err := rows.Scan(&v.ID, &v.Name, &v.Path); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func jsonText(v []string) string { b, _ := json.Marshal(v); return string(b) }
func parseStrings(v string) []string {
	var out []string
	_ = json.Unmarshal([]byte(v), &out)
	return out
}

func (s *Store) BumpVersion(libraryID int64) error {
	_, err := s.db.Exec(`INSERT INTO kv(key,value) VALUES('g:version','1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`)
	if err != nil {
		return err
	}
	s.gversion.Add(1)
	_, err = s.db.Exec(`INSERT INTO kv(key,value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`, "lib:"+strconv.FormatInt(libraryID, 10)+":version")
	return err
}
func (s *Store) Version(key string) string {
	if key == "g:version" {
		return strconv.FormatInt(s.gversion.Load(), 10)
	}
	var value string
	_ = s.db.QueryRow("SELECT value FROM kv WHERE key=?", key).Scan(&value)
	return value
}

// SetKV 写一条 kv（服务身份等长期配置用）。
func (s *Store) SetKV(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) UpsertMovie(m Movie, size int64, mtime time.Time) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	q := `INSERT INTO movies(library_id,source_path,file_size,file_mtime,source_protocol,source_container,number,status,nfo_path,output_dir,title,original_title,plot,year,premiered,rating,director,series,maker,label,collection,official_rating,sortname,taglines,provider_id,genres,tags,studios,poster_path,backdrop_path,landscape_path,runtime_seconds,additional_parts,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_path) DO UPDATE SET library_id=excluded.library_id,file_size=excluded.file_size,file_mtime=excluded.file_mtime,source_protocol=excluded.source_protocol,source_container=excluded.source_container,number=excluded.number,status=excluded.status,nfo_path=excluded.nfo_path,output_dir=excluded.output_dir,title=excluded.title,original_title=excluded.original_title,plot=excluded.plot,year=excluded.year,premiered=excluded.premiered,rating=excluded.rating,director=excluded.director,series=excluded.series,maker=excluded.maker,label=excluded.label,collection=excluded.collection,official_rating=excluded.official_rating,sortname=excluded.sortname,taglines=excluded.taglines,provider_id=excluded.provider_id,genres=excluded.genres,tags=excluded.tags,studios=excluded.studios,poster_path=excluded.poster_path,backdrop_path=excluded.backdrop_path,landscape_path=excluded.landscape_path,runtime_seconds=excluded.runtime_seconds,additional_parts=excluded.additional_parts,updated_at=excluded.updated_at`
	_, err := s.db.Exec(q, m.LibraryID, m.SourcePath, size, mtime.UTC().Format(time.RFC3339), m.SourceProtocol, m.SourceContainer, m.Number, m.Status, m.NFOPath, m.OutputDir, m.Title, m.OriginalTitle, m.Plot, m.Year, m.Premiere, m.Rating, m.Director, m.Series, m.Maker, m.Label, m.Collection, m.OfficialRating, m.SortName, jsonText(m.Taglines), m.ProviderID, jsonText(m.Genres), jsonText(m.Tags), jsonText(m.Studios), m.PosterPath, m.BackdropPath, m.LandscapePath, m.RuntimeSeconds, jsonText(m.AdditionalParts), now, now)
	if err != nil {
		return 0, err
	}
	return s.MovieIDByPath(m.SourcePath)
}
func (s *Store) MovieIDByPath(path string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM movies WHERE source_path=?", path).Scan(&id)
	return id, err
}

func (s *Store) DeleteMovie(id int64) error {
	result, err := s.db.Exec("DELETE FROM movies WHERE id=?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteLibrary 删除媒体库及其影片索引（仅删库内记录，不触碰磁盘文件）。
// userdata / movie_actors 通过外键 ON DELETE CASCADE 随影片一并清理。
func (s *Store) DeleteLibrary(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM movies WHERE library_id=?", id); err != nil {
		_ = tx.Rollback()
		return err
	}
	result, err := tx.Exec("DELETE FROM libraries WHERE id=?", id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if affected == 0 {
		_ = tx.Rollback()
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) DeleteMissingSources(libraryID int64, paths map[string]struct{}) error {
	rows, err := s.db.Query("SELECT source_path FROM movies WHERE library_id=?", libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if _, ok := paths[path]; !ok {
			missing = append(missing, path)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, path := range missing {
		if _, err := s.db.Exec("DELETE FROM movies WHERE library_id=? AND source_path=?", libraryID, path); err != nil {
			return err
		}
	}
	return nil
}

func movieScan(row *sql.Rows) (Movie, error) {
	var m Movie
	var genres, tags, studios, taglines, parts, mt string
	err := row.Scan(&m.ID, &m.LibraryID, &m.SourcePath, &mt, &m.SourceProtocol, &m.SourceContainer, &m.Number, &m.Status, &m.NFOPath, &m.OutputDir, &m.Title, &m.OriginalTitle, &m.Plot, &m.Year, &m.Premiere, &m.Rating, &m.Director, &m.Series, &m.Maker, &m.Label, &m.Collection, &m.OfficialRating, &m.SortName, &taglines, &m.ProviderID, &genres, &tags, &studios, &m.PosterPath, &m.BackdropPath, &m.LandscapePath, &m.RuntimeSeconds, &parts, &m.CreatedAt, &m.UpdatedAt, &m.LastScrapeAt, &m.LastScrapeError)
	m.Genres = parseStrings(genres)
	m.Tags = parseStrings(tags)
	m.Studios = parseStrings(studios)
	m.Taglines = parseStrings(taglines)
	m.AdditionalParts = parseStrings(parts)
	return m, err
}

const movieCols = "id,library_id,source_path,file_mtime,source_protocol,source_container,number,status,nfo_path,output_dir,title,original_title,plot,year,premiered,rating,director,series,maker,label,COALESCE(collection,''),COALESCE(official_rating,''),COALESCE(sortname,''),COALESCE(taglines,''),COALESCE(provider_id,''),genres,tags,studios,poster_path,backdrop_path,landscape_path,runtime_seconds,COALESCE(additional_parts,'[]'),COALESCE(created_at,''),COALESCE(updated_at,''),COALESCE(last_scrape_at,''),COALESCE(last_scrape_error,'')"

func (s *Store) Movie(id int64) (Movie, error) {
	row, err := s.db.Query("SELECT "+movieCols+" FROM movies WHERE id=?", id)
	if err != nil {
		return Movie{}, err
	}
	defer row.Close()
	if !row.Next() {
		return Movie{}, sql.ErrNoRows
	}
	return movieScan(row)
}
func (s *Store) SearchFiltered(libraryID int64, term, years, genre string, unplayed bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, "", years, genre, "", "", "", "", "", "", unplayed, false, sortBy, desc, limit, offset)
}

// SearchScoped 供合集上下文检索：collection="*" 限定“属于任一合集”，
// 非空串限定为某个具体合集，空串表示不限合集。
func (s *Store) SearchScoped(libraryID int64, collection, term, years, genre, tags, studios, person string, unplayed, favorite bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, "", years, genre, tags, studios, person, collection, "", "", unplayed, favorite, sortBy, desc, limit, offset)
}

func (s *Store) SearchAll(libraryID int64, term, status, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, status, "", "", "", "", "", "", "", "", false, false, sortBy, desc, limit, offset)
}

// AdminQuery 管理端列表（媒体墙）的查询条件。
//
// 用结构体而非长参数表：媒体墙的筛选维度会随详情抽屉的实体跳转继续增加，
// 位置参数在调用点极易错位。全部过滤与分页都在 SQL 层完成，
// 避免先分页再过滤导致每页条数与总数失真。
type AdminQuery struct {
	LibraryID  int64  // 0 = 全部媒体库
	Term       string // 标题/番号/原名模糊匹配
	Status     string // "" = 可播放+手动录入
	Protocol   string // source_protocol
	Genre      string
	Tag        string
	Studio     string
	Person     string
	Collection string // ""=不限；"*"=任一合集成员；其它=具体合集名
	// Scrape 按刮削结果筛选：""=不限；"failed"=刮削失败；"confirm"=待人工确认。
	Scrape string
	SortBy string
	Desc   bool
	Limit  int
	Offset int
}

// SearchAdmin 供管理端列表使用。
func (s *Store) SearchAdmin(q AdminQuery) ([]Movie, int, error) {
	return s.search(q.LibraryID, q.Term, q.Status, "", q.Genre, q.Tag, q.Studio, q.Person, q.Collection, q.Protocol, q.Scrape,
		false, false, q.SortBy, q.Desc, q.Limit, q.Offset)
}

// MoviesForProbe 返回媒体信息探测的候选影片，只挑有 NFO 的条目：
// 探测结果写回 NFO，没有 NFO 的条目（pending）不在其职责范围内——写入 NFO 会让
// 下次扫库把它误判为 success，破坏「扫描只负责入库」的边界。
//
// 同时取出 additional_parts：分集影片的每个分段都有自己的 .strm，
// 需要逐个探测、各自留下 mediainfo.json，故调用方要能枚举出全部分段文件。
// status 为空表示不限状态；limit<=0 表示不限条数。
func (s *Store) MoviesForProbe(libraryID int64, status string, limit int) ([]Movie, error) {
	query := "SELECT id,library_id,nfo_path,source_path,title,COALESCE(additional_parts,'[]') FROM movies WHERE COALESCE(nfo_path,'')<>''"
	args := []any{}
	if libraryID > 0 {
		query += " AND library_id=?"
		args = append(args, libraryID)
	}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY id"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		var m Movie
		var parts string
		if err := rows.Scan(&m.ID, &m.LibraryID, &m.NFOPath, &m.SourcePath, &m.Title, &parts); err != nil {
			return nil, err
		}
		m.AdditionalParts = parseStrings(parts)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) search(libraryID int64, term, status, years, genre, tags, studios, person, collection, protocol, scrape string, unplayed, favorite bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	where := []string{}
	args := []any{}
	switch status {
	case "":
		// 默认口径：只列「能看的」，pending/incompatible 需要显式筛选。
		where = append(where, "status IN ('success','manual')")
	case StatusAll:
		// 明确要求不限状态（如按刮削失败筛选，那些条目多半还是 pending）。
	default:
		where = append(where, "status=?")
		args = append(args, status)
	}
	if libraryID > 0 {
		where = append(where, "library_id=?")
		args = append(args, libraryID)
	}
	if collection == "*" {
		where = append(where, "COALESCE(collection,'') <> ''")
	} else if collection != "" {
		where = append(where, "collection=?")
		args = append(args, collection)
	}
	if protocol != "" {
		where = append(where, "source_protocol=?")
		args = append(args, protocol)
	}
	switch scrape {
	case ScrapeFilterFailed:
		// 失败与待确认都记在 last_scrape_error 上，用前缀区分（见 SetScrapeResult 的约定）。
		where = append(where, "COALESCE(last_scrape_error,'') <> '' AND last_scrape_error NOT LIKE ?")
		args = append(args, ScrapeConfirmPrefix+"%")
	case ScrapeFilterConfirm:
		where = append(where, "last_scrape_error LIKE ?")
		args = append(args, ScrapeConfirmPrefix+"%")
	}
	if term != "" {
		where = append(where, "(title LIKE ? OR original_title LIKE ? OR number LIKE ?)")
		q := "%" + term + "%"
		args = append(args, q, q, q)
	}
	if years != "" {
		parts := strings.Split(years, ",")
		placeholders := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				placeholders = append(placeholders, "?")
				args = append(args, part)
			}
		}
		if len(placeholders) > 0 {
			where = append(where, "CAST(year AS TEXT) IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	for field, value := range map[string]string{"genres": genre, "tags": tags, "studios": studios} {
		if value == "" {
			continue
		}
		terms := splitTerms(value)
		if len(terms) == 0 {
			continue
		}
		clauses := make([]string, 0, len(terms))
		for _, t := range terms {
			clauses = append(clauses, field+" LIKE ?")
			args = append(args, "%\""+strings.ReplaceAll(t, `"`, "")+"\"%")
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}
	if person != "" {
		terms := splitTerms(person)
		if len(terms) > 0 {
			clauses := make([]string, 0, len(terms))
			for _, t := range terms {
				clauses = append(clauses, "EXISTS (SELECT 1 FROM movie_actors ma WHERE ma.movie_id=movies.id AND ma.actor_name LIKE ?)")
				args = append(args, t)
			}
			where = append(where, "("+strings.Join(clauses, " OR ")+")")
		}
	}
	if unplayed {
		where = append(where, "NOT EXISTS (SELECT 1 FROM userdata WHERE userdata.movie_id=movies.id AND userdata.played=1)")
	}
	if favorite {
		where = append(where, "EXISTS (SELECT 1 FROM userdata WHERE userdata.movie_id=movies.id AND userdata.is_favorite=1)")
	}
	// status=StatusAll 时可能一条过滤条件都没有（此时 where 为空），
	// 必须整段省掉 WHERE，否则会拼出 "SELECT ... FROM movies WHERE " 的残缺 SQL。
	cond := strings.Join(where, " AND ")
	clause := ""
	if cond != "" {
		clause = " WHERE " + cond
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movies"+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	allowed := map[string]string{
		"title": "title", "year": "year", "date": "premiered", "sortname": "title",
		"datecreated": "id", "random": "title", "communityrating": "rating",
	}
	column := allowed[strings.ToLower(sortBy)]
	if column == "" {
		column = "title"
	}
	direction := "ASC"
	if desc {
		direction = "DESC"
	}
	args = append(args, limit, offset)
	rows, err := s.db.Query("SELECT "+movieCols+" FROM movies"+clause+" ORDER BY "+column+" "+direction+",id LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, e := movieScan(rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}
func (s *Store) Data(id int64) (UserData, error) {
	var d UserData
	var played, favorite, likes, hidden int
	err := s.db.QueryRow(`SELECT position_ticks,play_count,played,COALESCE(last_played_at,''),COALESCE(last_stopped_ticks,-1),
		COALESCE(is_favorite,0),COALESCE(likes,0),COALESCE(hide_from_resume,0) FROM userdata WHERE movie_id=?`, id).Scan(&d.PositionTicks, &d.PlayCount, &played, &d.LastPlayedAt, &d.StoppedTicks, &favorite, &likes, &hidden)
	d.Played = played != 0
	d.IsFavorite = favorite != 0
	d.Likes = likes
	d.HideFromResume = hidden != 0
	if err == sql.ErrNoRows {
		return UserData{}, nil
	}
	return d, err
}

// DataFor 批量读取多部影片的 UserData（一次 IN 查询，替代列表页逐片 Data() 的 N+1）。
func (s *Store) DataFor(ids []int64) (map[int64]UserData, error) {
	out := make(map[int64]UserData, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT movie_id,position_ticks,play_count,played,COALESCE(last_played_at,''),COALESCE(last_stopped_ticks,-1),
		COALESCE(is_favorite,0),COALESCE(likes,0),COALESCE(hide_from_resume,0) FROM userdata WHERE movie_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d UserData
		var id int64
		var played, favorite, likes, hidden int
		if err := rows.Scan(&id, &d.PositionTicks, &d.PlayCount, &played, &d.LastPlayedAt, &d.StoppedTicks, &favorite, &likes, &hidden); err != nil {
			return nil, err
		}
		d.Played = played != 0
		d.IsFavorite = favorite != 0
		d.Likes = likes
		d.HideFromResume = hidden != 0
		out[id] = d
	}
	return out, rows.Err()
}

// ActorRef 演员及其头像索引。
//
// AvatarURL 来自 NFO 的 <actor><thumb>（远端地址，真源），AvatarTag 是本地副本
// avatars/<hash>.webp 的内容标识，二者都为空表示该演员尚无头像。
type ActorRef struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url,omitempty"`
	AvatarTag string `json:"avatar_tag,omitempty"`
}

// ActorsFor 批量读取多部影片的演员列表（一次 IN 查询，替代列表页逐片 Actors() 的 N+1），
// 同时带回头像字段——列表页的 People[] 需要 PrimaryImageTag，逐片回查会退化成 N+1。
func (s *Store) ActorsFor(ids []int64) (map[int64][]ActorRef, error) {
	out := make(map[int64][]ActorRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT ma.movie_id, ma.actor_name, COALESCE(a.avatar_url,''), COALESCE(a.avatar_tag,'')
		FROM movie_actors ma LEFT JOIN actors a ON a.name=ma.actor_name
		WHERE ma.movie_id IN (`+placeholders+`) ORDER BY ma.movie_id, ma.actor_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var actor ActorRef
		if err := rows.Scan(&id, &actor.Name, &actor.AvatarURL, &actor.AvatarTag); err != nil {
			return nil, err
		}
		out[id] = append(out[id], actor)
	}
	return out, rows.Err()
}

// ActorAvatar 按演员名查头像索引；查无此人时返回零值（不报错）。
func (s *Store) ActorAvatar(name string) (ActorRef, error) {
	var actor ActorRef
	err := s.db.QueryRow("SELECT name, COALESCE(avatar_url,''), COALESCE(avatar_tag,'') FROM actors WHERE name=?",
		strings.TrimSpace(name)).Scan(&actor.Name, &actor.AvatarURL, &actor.AvatarTag)
	if err == sql.ErrNoRows {
		return ActorRef{}, nil
	}
	return actor, err
}

// SetActorAvatar 只更新头像索引（供头像任务写入，不动影片-演员关系）。
// tag 为空表示暂无本地副本，此时保留原有标识不变。
func (s *Store) SetActorAvatar(name, url, tag string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO actors(name,avatar_url,avatar_tag,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			avatar_url=CASE WHEN excluded.avatar_url<>'' THEN excluded.avatar_url ELSE actors.avatar_url END,
			avatar_tag=CASE WHEN excluded.avatar_tag<>'' THEN excluded.avatar_tag ELSE actors.avatar_tag END`,
		name, url, tag, time.Now().UTC().Format(time.RFC3339))
	return err
}

// b2i 把布尔值转成 SQLite 的 0/1。
func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// SavePlayback 只写播放相关列。播放上报与各 setter 各写各的列，
// 避免「读整行→改一列→写回整行」时用旧值覆盖并发的收藏/评分变更。
func (s *Store) SavePlayback(id int64, positionTicks, playCount int64, lastPlayedAt string, stoppedTicks int64) error {
	_, err := s.db.Exec(`INSERT INTO userdata(movie_id,position_ticks,play_count,last_played_at,last_stopped_ticks)
		VALUES(?,?,?,?,?)
		ON CONFLICT(movie_id) DO UPDATE SET position_ticks=excluded.position_ticks,play_count=excluded.play_count,
		last_played_at=excluded.last_played_at,last_stopped_ticks=excluded.last_stopped_ticks`,
		id, positionTicks, playCount, lastPlayedAt, stoppedTicks)
	return err
}

// SetPlayed 只更新已播标记。
func (s *Store) SetPlayed(id int64, played bool) error {
	_, err := s.db.Exec(`INSERT INTO userdata(movie_id,played) VALUES(?,?)
		ON CONFLICT(movie_id) DO UPDATE SET played=excluded.played`, id, b2i(played))
	return err
}

// SetFavorite 收藏/取消收藏。
func (s *Store) SetFavorite(id int64, favorite bool) error {
	_, err := s.db.Exec(`INSERT INTO userdata(movie_id,is_favorite) VALUES(?,?)
		ON CONFLICT(movie_id) DO UPDATE SET is_favorite=excluded.is_favorite`, id, b2i(favorite))
	return err
}

// SetLikes 个人评分：1 赞 / -1 踩 / 0 清除。
func (s *Store) SetLikes(id int64, likes int) error {
	_, err := s.db.Exec(`INSERT INTO userdata(movie_id,likes) VALUES(?,?)
		ON CONFLICT(movie_id) DO UPDATE SET likes=excluded.likes`, id, likes)
	return err
}

// SetHideFromResume 隐藏/恢复续播。
func (s *Store) SetHideFromResume(id int64, hidden bool) error {
	_, err := s.db.Exec(`INSERT INTO userdata(movie_id,hide_from_resume) VALUES(?,?)
		ON CONFLICT(movie_id) DO UPDATE SET hide_from_resume=excluded.hide_from_resume`, id, b2i(hidden))
	return err
}
func (s *Store) Probe(method, path, query, body string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO api_probe(method,path,query,body_preview,created_at) VALUES(?,?,?,?,?)", method, path, query, body, time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err = tx.Exec("DELETE FROM api_probe WHERE id NOT IN (SELECT id FROM api_probe ORDER BY id DESC LIMIT 1000)"); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) ClearProbes() error {
	_, err := s.db.Exec("DELETE FROM api_probe")
	return err
}

// ReplaceActors 覆盖一部影片的演员关系，并把 NFO 带来的头像地址同步进 actors 表。
// 传入 avatar 为空时不清空已有头像——扫库多数时候只读到姓名，
// 不能让一次扫描把头像任务已写入的索引抹掉。
func (s *Store) ReplaceActors(movieID int64, actors []ActorRef) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM movie_actors WHERE movie_id=?", movieID); err != nil {
		_ = tx.Rollback()
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, actor := range actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO actors(name,avatar_url,updated_at) VALUES(?,?,?)
			ON CONFLICT(name) DO UPDATE SET
				avatar_url=CASE WHEN excluded.avatar_url<>'' THEN excluded.avatar_url ELSE actors.avatar_url END,
				updated_at=excluded.updated_at`,
			name, strings.TrimSpace(actor.AvatarURL), now); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO movie_actors(movie_id,actor_name) VALUES(?,?)", movieID, name); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Actors 返回一部影片的演员（含头像索引），按姓名排序。
func (s *Store) Actors(movieID int64) ([]ActorRef, error) {
	rows, err := s.db.Query(`SELECT ma.actor_name, COALESCE(a.avatar_url,''), COALESCE(a.avatar_tag,'')
		FROM movie_actors ma LEFT JOIN actors a ON a.name=ma.actor_name
		WHERE ma.movie_id=? ORDER BY ma.actor_name`, movieID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActorRef
	for rows.Next() {
		var actor ActorRef
		if err := rows.Scan(&actor.Name, &actor.AvatarURL, &actor.AvatarTag); err != nil {
			return nil, err
		}
		out = append(out, actor)
	}
	return out, rows.Err()
}

// AllActors 一次返回全部可见影片的演员映射，供相似度批量打分与相似列表组装
// 演员清单（带头像索引，避免再查一次）。
func (s *Store) AllActors() (map[int64][]ActorRef, error) {
	rows, err := s.db.Query(`SELECT ma.movie_id, ma.actor_name, COALESCE(a.avatar_url,''), COALESCE(a.avatar_tag,'')
		FROM movie_actors ma
		JOIN movies ON movies.id=ma.movie_id
		LEFT JOIN actors a ON a.name=ma.actor_name
		WHERE movies.status IN ('success','manual')
		ORDER BY ma.actor_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]ActorRef)
	for rows.Next() {
		var movieID int64
		var actor ActorRef
		if err := rows.Scan(&movieID, &actor.Name, &actor.AvatarURL, &actor.AvatarTag); err != nil {
			return nil, err
		}
		out[movieID] = append(out[movieID], actor)
	}
	return out, rows.Err()
}

// Latest 返回库内最近入库的可见影片（按入库倒序）。
func (s *Store) Latest(libraryID int64, limit, offset int) ([]Movie, error) {
	query := "SELECT " + movieCols + " FROM movies WHERE status IN ('success','manual')"
	args := []any{}
	if libraryID > 0 {
		query += " AND library_id=?"
		args = append(args, libraryID)
	}
	query += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := movieScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Resumed 返回有播放进度且未隐藏续播的可见影片（按标题排序，与列表口径一致），
// 供「继续观看」直接分页，避免全量载入后在内存里过滤。
func (s *Store) Resumed(limit, offset int) ([]Movie, int, error) {
	const cond = ` FROM movies JOIN userdata u ON u.movie_id=movies.id
		WHERE movies.status IN ('success','manual') AND u.position_ticks>0 AND COALESCE(u.hide_from_resume,0)=0`
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*)" + cond).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query("SELECT "+movieCols+cond+" ORDER BY movies.title, movies.id LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := movieScan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// CountByStatus 统计某状态的影片数（管理端总览用，避免为计数拉回整批影片）。
func (s *Store) CountByStatus(status string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM movies WHERE status=?", status).Scan(&count)
	return count, err
}

func (s *Store) Probes(limit int) ([]map[string]any, error) {
	rows, err := s.db.Query("SELECT id,method,path,query,body_preview,created_at FROM api_probe ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var method, path, query, body, created string
		if err := rows.Scan(&id, &method, &path, &query, &body, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "method": method, "path": path, "query": query, "body": body, "created_at": created})
	}
	return out, rows.Err()
}
func NotFound(err error) bool   { return err == sql.ErrNoRows }
func (m Movie) IsVisible() bool { return m.Status == "success" || m.Status == "manual" }

// splitTerms 把查询参数拆成去空白的项。Emby 的 Genres/Tags/Studios 形如 "a|b"，
// GenreIds/PersonIds 等形如 "a,b"；这里两种分隔符都接受。
func splitTerms(value string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(value, "|", ","), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// distinctStrings 对可见影片某 JSON 列（genres/tags/studios）做去重。
// collection："" 不限；"*" 限定“属于任一合集”；其它值限定具体合集。
func (s *Store) distinctStrings(libraryID int64, collection, column string) ([]string, error) {
	query := "SELECT " + column + " FROM movies WHERE status IN ('success','manual')"
	args := []any{}
	if libraryID > 0 {
		query += " AND library_id=?"
		args = append(args, libraryID)
	}
	if collection == "*" {
		query += " AND COALESCE(collection,'') <> ''"
	} else if collection != "" {
		query += " AND collection=?"
		args = append(args, collection)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		for _, v := range parseStrings(raw) {
			if v = strings.TrimSpace(v); v != "" {
				seen[v] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out, nil
}

// Genres 返回可见影片的流派名集合（可按媒体库/合集范围过滤）。
func (s *Store) Genres(libraryID int64, collection string) ([]string, error) {
	return s.distinctStrings(libraryID, collection, "genres")
}

// Tags 返回可见影片的标签名集合（可按媒体库/合集范围过滤）。
func (s *Store) Tags(libraryID int64, collection string) ([]string, error) {
	return s.distinctStrings(libraryID, collection, "tags")
}

// Studios 返回可见影片的制片商集合（可按媒体库/合集范围过滤）。
func (s *Store) Studios(libraryID int64, collection string) ([]string, error) {
	return s.distinctStrings(libraryID, collection, "studios")
}

// Persons 返回可见影片涉及的演员名集合（可按媒体库/合集范围过滤）。
func (s *Store) Persons(libraryID int64, collection string) ([]string, error) {
	query := `SELECT DISTINCT ma.actor_name FROM movie_actors ma
		JOIN movies ON movies.id=ma.movie_id
		WHERE movies.status IN ('success','manual')`
	args := []any{}
	if libraryID > 0 {
		query += " AND movies.library_id=?"
		args = append(args, libraryID)
	}
	if collection == "*" {
		query += " AND COALESCE(movies.collection,'') <> ''"
	} else if collection != "" {
		query += " AND movies.collection=?"
		args = append(args, collection)
	}
	query += " ORDER BY ma.actor_name"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Collections 返回可见影片去重后的合集名（来自 NFO <set><name>）。
func (s *Store) Collections() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT collection FROM movies
		WHERE status IN ('success','manual') AND COALESCE(collection,'') <> ''
		ORDER BY collection`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// entityColumns 实体类型 → movies 上存实体的 JSON 列。
var entityColumns = map[string]string{
	"Genre":  "genres",
	"Tag":    "tags",
	"Studio": "studios",
}

// EntityPoster 为某个实体（Genre/Tag/Studio/Person）挑一部最近入库且有海报的影片
// 作为代表性封面，返回其 poster 路径。collection 可限定合集范围（""/"*"/具体名）。
func (s *Store) EntityPoster(kind, name string, libraryID int64, collection string) (string, error) {
	query := `SELECT poster_path FROM movies
		WHERE status IN ('success','manual') AND poster_path<>''`
	args := []any{}
	if libraryID > 0 {
		query += " AND library_id=?"
		args = append(args, libraryID)
	}
	if collection == "*" {
		query += " AND COALESCE(collection,'') <> ''"
	} else if collection != "" {
		query += " AND collection=?"
		args = append(args, collection)
	}
	if kind == "Person" {
		query += " AND EXISTS (SELECT 1 FROM movie_actors ma WHERE ma.movie_id=movies.id AND ma.actor_name=?)"
		args = append(args, name)
	} else {
		column, ok := entityColumns[kind]
		if !ok {
			return "", nil
		}
		query += " AND " + column + " LIKE ?"
		args = append(args, "%\""+strings.ReplaceAll(name, `"`, "")+"\"%")
	}
	query += " ORDER BY id DESC LIMIT 1"
	var path string
	err := s.db.QueryRow(query, args...).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}

// RepresentativeArt 返回媒体库“主视觉”路径：优先取最近入库且带 fanart 的宽图，
// 无宽图则回退 poster。对应 4.9 媒体库 auto_poster 用宽图当封面的观感。
func (s *Store) RepresentativeArt(libraryID int64) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT backdrop_path FROM movies
		WHERE library_id=? AND status IN ('success','manual') AND backdrop_path<>''
		ORDER BY id DESC LIMIT 1`, libraryID).Scan(&path)
	if err == nil {
		return path, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	err = s.db.QueryRow(`SELECT poster_path FROM movies
		WHERE library_id=? AND status IN ('success','manual') AND poster_path<>''
		ORDER BY id DESC LIMIT 1`, libraryID).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}

// CollectionPoster 返回某合集中最近入库且带海报的影片海报路径（用作合集封面）。
func (s *Store) CollectionPoster(collection string) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT poster_path FROM movies
		WHERE collection=? AND status IN ('success','manual') AND poster_path<>''
		ORDER BY id DESC LIMIT 1`, collection).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}

// CollectionStat 合集的聚合信息：影片数、代表海报、去重流派。
type CollectionStat struct {
	Count  int
	Poster string
	Genres []string
}

// CollectionStats 批量返回各合集的聚合信息，供合集列表一次取回，
// 避免逐项调 CollectionSummary/CollectionPoster 造成的 N+1 查询。
func (s *Store) CollectionStats() (map[string]CollectionStat, error) {
	rows, err := s.db.Query(`SELECT m1.collection, COUNT(*),
		COALESCE((SELECT m2.poster_path FROM movies m2
			WHERE m2.collection=m1.collection AND m2.status IN ('success','manual') AND m2.poster_path<>''
			ORDER BY m2.id DESC LIMIT 1),'')
		FROM movies m1
		WHERE m1.status IN ('success','manual') AND COALESCE(m1.collection,'')<>''
		GROUP BY m1.collection`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]CollectionStat)
	for rows.Next() {
		var name, poster string
		var count int
		if err := rows.Scan(&name, &count, &poster); err != nil {
			return nil, err
		}
		out[name] = CollectionStat{Count: count, Poster: poster}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 流派存在每行的 JSON 列里，单独取两列后在内存聚合去重。
	rows2, err := s.db.Query(`SELECT collection, genres FROM movies
		WHERE status IN ('success','manual') AND COALESCE(collection,'')<>''`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	genres := make(map[string]map[string]struct{})
	for rows2.Next() {
		var name, raw string
		if err := rows2.Scan(&name, &raw); err != nil {
			return nil, err
		}
		for _, g := range parseStrings(raw) {
			if g = strings.TrimSpace(g); g != "" {
				if genres[name] == nil {
					genres[name] = make(map[string]struct{})
				}
				genres[name][g] = struct{}{}
			}
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}
	for name, set := range genres {
		stat := out[name]
		for g := range set {
			stat.Genres = append(stat.Genres, g)
		}
		out[name] = stat
	}
	return out, nil
}

// CollectionSummary 返回合集内可见影片数与该合集聚合出的流派（去重）。
func (s *Store) CollectionSummary(collection string) (count int, genres []string, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM movies
		WHERE collection=? AND status IN ('success','manual')`, collection).Scan(&count); err != nil {
		return 0, nil, err
	}
	rows, err := s.db.Query(`SELECT genres FROM movies
		WHERE collection=? AND status IN ('success','manual')`, collection)
	if err != nil {
		return count, nil, err
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return count, nil, err
		}
		for _, g := range parseStrings(raw) {
			if g = strings.TrimSpace(g); g != "" {
				seen[g] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return count, nil, err
	}
	for g := range seen {
		genres = append(genres, g)
	}
	return count, genres, nil
}

// UnplayedInLibrary 统计库内可见且未标记已播的影片数。
func (s *Store) UnplayedInLibrary(libraryID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM movies WHERE library_id=? AND status IN ('success','manual')
		AND NOT EXISTS (SELECT 1 FROM userdata u WHERE u.movie_id=movies.id AND u.played=1)`, libraryID).Scan(&count)
	return count, err
}

// —— 计划任务（cron）：定义存 DB，执行历史不落库，只保留「上次结果」 ——

// ScheduledTask 一条计划任务定义。
//
// Params 为任务参数（JSON 文本，缺省 "{}"），具体字段由任务类型决定；
// 用单列存 JSON 是为了新增任务类型时不必改表。
type ScheduledTask struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Cron        string `json:"cron"`
	Params      string `json:"params"`
	Enabled     bool   `json:"enabled"`
	LastRunAt   string `json:"last_run_at,omitempty"`
	LastStatus  string `json:"last_status,omitempty"`  // success / failed / skipped
	LastMessage string `json:"last_message,omitempty"` // 失败原因或跳过原因
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

const scheduledColumns = "id,name,type,cron,params,enabled,last_run_at,last_status,last_message,created_at,updated_at"

func scanScheduled(row interface{ Scan(...any) error }) (ScheduledTask, error) {
	var v ScheduledTask
	var lastRun, lastStatus, lastMessage sql.NullString
	err := row.Scan(&v.ID, &v.Name, &v.Type, &v.Cron, &v.Params, &v.Enabled,
		&lastRun, &lastStatus, &lastMessage, &v.CreatedAt, &v.UpdatedAt)
	v.LastRunAt, v.LastStatus, v.LastMessage = lastRun.String, lastStatus.String, lastMessage.String
	return v, err
}

// ScheduledTasks 返回全部计划任务（含已禁用），按 id 排序。
func (s *Store) ScheduledTasks() ([]ScheduledTask, error) {
	rows, err := s.db.Query("SELECT " + scheduledColumns + " FROM scheduled_tasks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledTask
	for rows.Next() {
		v, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ScheduledTask 按 id 返回单条计划任务。
func (s *Store) ScheduledTask(id int64) (ScheduledTask, error) {
	return scanScheduled(s.db.QueryRow("SELECT "+scheduledColumns+" FROM scheduled_tasks WHERE id=?", id))
}

// CreateScheduledTask 新建计划任务并返回落库后的完整记录。
func (s *Store) CreateScheduledTask(v ScheduledTask) (ScheduledTask, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if v.Params == "" {
		v.Params = "{}"
	}
	result, err := s.db.Exec(`INSERT INTO scheduled_tasks(name,type,cron,params,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)`, v.Name, v.Type, v.Cron, v.Params, v.Enabled, now, now)
	if err != nil {
		return ScheduledTask{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return ScheduledTask{}, err
	}
	return s.ScheduledTask(id)
}

// UpdateScheduledTask 覆盖计划任务的可编辑字段（保留上次执行结果）。
func (s *Store) UpdateScheduledTask(v ScheduledTask) (ScheduledTask, error) {
	if v.Params == "" {
		v.Params = "{}"
	}
	result, err := s.db.Exec(`UPDATE scheduled_tasks SET name=?,type=?,cron=?,params=?,enabled=?,updated_at=? WHERE id=?`,
		v.Name, v.Type, v.Cron, v.Params, v.Enabled, time.Now().UTC().Format(time.RFC3339), v.ID)
	if err != nil {
		return ScheduledTask{}, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return ScheduledTask{}, err
	} else if count == 0 {
		return ScheduledTask{}, sql.ErrNoRows
	}
	return s.ScheduledTask(v.ID)
}

// SetScheduledTaskEnabled 只切换启用状态（列表页开关用，避免整体覆盖）。
func (s *Store) SetScheduledTaskEnabled(id int64, enabled bool) error {
	result, err := s.db.Exec(`UPDATE scheduled_tasks SET enabled=?,updated_at=? WHERE id=?`,
		enabled, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteScheduledTask(id int64) error {
	result, err := s.db.Exec("DELETE FROM scheduled_tasks WHERE id=?", id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordScheduledResult 记录一次执行的结束状态（status: success/failed/skipped）。
func (s *Store) RecordScheduledResult(id int64, status, message string) error {
	_, err := s.db.Exec(`UPDATE scheduled_tasks SET last_run_at=?,last_status=?,last_message=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), status, message, id)
	return err
}

// —— 设置项（刮削器等运行期配置）：只存已改过的键，缺省值由代码兜底 ——

// Setting 读取一条设置；不存在时返回空串（不报错，便于「缺省走代码默认」）。
func (s *Store) Setting(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetSetting 写入一条设置。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Settings 返回全部设置项，供管理端回显。
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query("SELECT key,value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

// —— 刮削（R4）：目标选取、结果记录、头像任务候选 ——

// StatusAll 是「不限状态」的哨兵值：search 的默认口径只列 success/manual，
// 需要看到 pending（刮削失败/待确认的条目）时必须显式传它，而不是传空串。
const StatusAll = "__all__"

// 刮削结果的筛选值（AdminQuery.Scrape）。
const (
	ScrapeFilterFailed  = "failed"
	ScrapeFilterConfirm = "confirm"
)

// ScrapeConfirmPrefix 批量刮削「非番号精确命中」的待人工确认标记前缀。
// 与真实失败共用 last_scrape_error 一列（需求 5f：不新增 status 枚举），
// 靠这个前缀把「需要人工介入」与「任务出错」区分开。
const ScrapeConfirmPrefix = "[待人工确认] "

// MoviesForScrape 返回刮削候选影片。
//
//   - libraryID=0 表示全部媒体库；
//   - onlyMissing=true 只挑元数据缺失的（无 NFO 或无标题），这是默认范围；
//   - 永远排除 incompatible——它们的源不是 http(s)，
//     写 NFO 也改变不了可播放性（判定只看 .strm 内容）。
//
// 顺带取出 additional_parts 与 nfo_path：刮削只写主 NFO，分段不单独处理。
func (s *Store) MoviesForScrape(libraryID int64, onlyMissing bool, limit int) ([]Movie, error) {
	query := "SELECT " + movieCols + " FROM movies WHERE status <> 'incompatible'"
	args := []any{}
	if libraryID > 0 {
		query += " AND library_id=?"
		args = append(args, libraryID)
	}
	if onlyMissing {
		query += " AND (COALESCE(nfo_path,'')='' OR COALESCE(title,'')='')"
	}
	query += " ORDER BY id"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := movieScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountMoviesForScrape 统计候选数量，供管理端展示「本次会处理多少条」。
func (s *Store) CountMoviesForScrape(libraryID int64, onlyMissing bool) (int, error) {
	query := "SELECT COUNT(*) FROM movies WHERE status <> 'incompatible'"
	args := []any{}
	if libraryID > 0 {
		query += " AND library_id=?"
		args = append(args, libraryID)
	}
	if onlyMissing {
		query += " AND (COALESCE(nfo_path,'')='' OR COALESCE(title,'')='')"
	}
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// SetScrapeResult 记录一次刮削的结束状态。message 为空表示成功（清空上次的错误）。
func (s *Store) SetScrapeResult(movieID int64, message string) error {
	_, err := s.db.Exec("UPDATE movies SET last_scrape_at=?, last_scrape_error=? WHERE id=?",
		time.Now().UTC().Format(time.RFC3339), message, movieID)
	return err
}

// ActorsMissingAvatar 返回还没有头像的演员名（供 scrape_avatars 任务）。
//
// 只挑至少参演过一部可见影片的演员：演员表里可能有历史遗留的孤立名字，
// 没有对应影片就没有写 NFO 头像真源的地方，处理了也留不住。
// libraryID>0 时只统计该库内的演员。
func (s *Store) ActorsMissingAvatar(libraryID int64, limit int) ([]string, error) {
	query := `SELECT DISTINCT a.name FROM actors a
		JOIN movie_actors ma ON ma.actor_name = a.name
		JOIN movies m ON m.id = ma.movie_id
		WHERE COALESCE(a.avatar_url,'')='' AND m.status IN ('success','manual')`
	args := []any{}
	if libraryID > 0 {
		query += " AND m.library_id=?"
		args = append(args, libraryID)
	}
	query += " ORDER BY a.name"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// MoviesByActor 返回某演员参演的可见影片（头像任务要逐个改写它们的 NFO）。
func (s *Store) MoviesByActor(name string) ([]Movie, error) {
	rows, err := s.db.Query("SELECT "+movieCols+` FROM movies
		WHERE status IN ('success','manual')
		  AND EXISTS (SELECT 1 FROM movie_actors ma WHERE ma.movie_id=movies.id AND ma.actor_name=?)
		ORDER BY id`, strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := movieScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
