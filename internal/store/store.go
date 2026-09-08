package store

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

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
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	_, err := s.db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS libraries (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, path TEXT UNIQUE NOT NULL, type TEXT DEFAULT 'movies', enabled INTEGER DEFAULT 1);
CREATE TABLE IF NOT EXISTS movies (id INTEGER PRIMARY KEY AUTOINCREMENT, library_id INTEGER NOT NULL, source_path TEXT UNIQUE NOT NULL, file_size INTEGER DEFAULT 0, file_mtime TEXT, source_protocol TEXT, source_container TEXT, number TEXT, status TEXT NOT NULL, nfo_path TEXT, output_dir TEXT, title TEXT, original_title TEXT, plot TEXT, year INTEGER, premiered TEXT, rating REAL, director TEXT, series TEXT, maker TEXT, label TEXT, collection TEXT, official_rating TEXT, sortname TEXT, taglines TEXT, provider_id TEXT, genres TEXT, tags TEXT, studios TEXT, poster_path TEXT, backdrop_path TEXT, landscape_path TEXT, runtime_seconds INTEGER DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY(library_id) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS userdata (movie_id INTEGER PRIMARY KEY REFERENCES movies(id) ON DELETE CASCADE, position_ticks INTEGER DEFAULT 0, play_count INTEGER DEFAULT 0, played INTEGER DEFAULT 0, last_played_at TEXT, last_stopped_ticks INTEGER DEFAULT -1, is_favorite INTEGER NOT NULL DEFAULT 0, likes INTEGER NOT NULL DEFAULT 0, hide_from_resume INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS api_probe (id INTEGER PRIMARY KEY AUTOINCREMENT, method TEXT, path TEXT, query TEXT, body_preview TEXT, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS actors (name TEXT PRIMARY KEY, avatar_url TEXT, updated_at TEXT);
CREATE TABLE IF NOT EXISTS movie_actors (movie_id INTEGER, actor_name TEXT, PRIMARY KEY(movie_id, actor_name), FOREIGN KEY(movie_id) REFERENCES movies(id) ON DELETE CASCADE, FOREIGN KEY(actor_name) REFERENCES actors(name));
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '0');
CREATE TABLE IF NOT EXISTS administrators (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS access_tokens (token TEXT PRIMARY KEY, created_at TEXT NOT NULL);`)
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
	_, err = s.db.Exec(`INSERT INTO kv(key,value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`, "lib:"+strconv.FormatInt(libraryID, 10)+":version")
	return err
}
func (s *Store) Version(key string) string {
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
	q := `INSERT INTO movies(library_id,source_path,file_size,file_mtime,source_protocol,source_container,number,status,nfo_path,output_dir,title,original_title,plot,year,premiered,rating,director,series,maker,label,collection,official_rating,sortname,taglines,provider_id,genres,tags,studios,poster_path,backdrop_path,landscape_path,runtime_seconds,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_path) DO UPDATE SET library_id=excluded.library_id,file_size=excluded.file_size,file_mtime=excluded.file_mtime,source_protocol=excluded.source_protocol,source_container=excluded.source_container,number=excluded.number,status=excluded.status,nfo_path=excluded.nfo_path,output_dir=excluded.output_dir,title=excluded.title,original_title=excluded.original_title,plot=excluded.plot,year=excluded.year,premiered=excluded.premiered,rating=excluded.rating,director=excluded.director,series=excluded.series,maker=excluded.maker,label=excluded.label,collection=excluded.collection,official_rating=excluded.official_rating,sortname=excluded.sortname,taglines=excluded.taglines,provider_id=excluded.provider_id,genres=excluded.genres,tags=excluded.tags,studios=excluded.studios,poster_path=excluded.poster_path,backdrop_path=excluded.backdrop_path,landscape_path=excluded.landscape_path,runtime_seconds=excluded.runtime_seconds,updated_at=excluded.updated_at`
	_, err := s.db.Exec(q, m.LibraryID, m.SourcePath, size, mtime.UTC().Format(time.RFC3339), m.SourceProtocol, m.SourceContainer, m.Number, m.Status, m.NFOPath, m.OutputDir, m.Title, m.OriginalTitle, m.Plot, m.Year, m.Premiere, m.Rating, m.Director, m.Series, m.Maker, m.Label, m.Collection, m.OfficialRating, m.SortName, jsonText(m.Taglines), m.ProviderID, jsonText(m.Genres), jsonText(m.Tags), jsonText(m.Studios), m.PosterPath, m.BackdropPath, m.LandscapePath, m.RuntimeSeconds, now, now)
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
	var genres, tags, studios, taglines, mt string
	err := row.Scan(&m.ID, &m.LibraryID, &m.SourcePath, &mt, &m.SourceProtocol, &m.SourceContainer, &m.Number, &m.Status, &m.NFOPath, &m.OutputDir, &m.Title, &m.OriginalTitle, &m.Plot, &m.Year, &m.Premiere, &m.Rating, &m.Director, &m.Series, &m.Maker, &m.Label, &m.Collection, &m.OfficialRating, &m.SortName, &taglines, &m.ProviderID, &genres, &tags, &studios, &m.PosterPath, &m.BackdropPath, &m.LandscapePath, &m.RuntimeSeconds)
	m.Genres = parseStrings(genres)
	m.Tags = parseStrings(tags)
	m.Studios = parseStrings(studios)
	m.Taglines = parseStrings(taglines)
	return m, err
}

const movieCols = "id,library_id,source_path,file_mtime,source_protocol,source_container,number,status,nfo_path,output_dir,title,original_title,plot,year,premiered,rating,director,series,maker,label,COALESCE(collection,''),COALESCE(official_rating,''),COALESCE(sortname,''),COALESCE(taglines,''),COALESCE(provider_id,''),genres,tags,studios,poster_path,backdrop_path,landscape_path,runtime_seconds"

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
func (s *Store) Search(libraryID int64, term string, limit, offset int) ([]Movie, int, error) {
	return s.SearchFiltered(libraryID, term, "", "", false, "title", false, limit, offset)
}
func (s *Store) SearchOrdered(libraryID int64, term, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.SearchFiltered(libraryID, term, "", "", false, sortBy, desc, limit, offset)
}
func (s *Store) SearchFiltered(libraryID int64, term, years, genre string, unplayed bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, "", years, genre, "", "", "", "", unplayed, false, sortBy, desc, limit, offset)
}

// SearchFilteredFull 带标签/制片商/演员/收藏过滤的检索，供 Emby Items 使用。
func (s *Store) SearchFilteredFull(libraryID int64, term, years, genre, tags, studios, person string, unplayed, favorite bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, "", years, genre, tags, studios, person, "", unplayed, favorite, sortBy, desc, limit, offset)
}

// SearchScoped 供合集上下文检索：collection="*" 限定“属于任一合集”，
// 非空串限定为某个具体合集，空串表示不限合集。
func (s *Store) SearchScoped(libraryID int64, collection, term, years, genre, tags, studios, person string, unplayed, favorite bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, "", years, genre, tags, studios, person, collection, unplayed, favorite, sortBy, desc, limit, offset)
}

func (s *Store) SearchAll(libraryID int64, term, status, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	return s.search(libraryID, term, status, "", "", "", "", "", "", false, false, sortBy, desc, limit, offset)
}
func (s *Store) search(libraryID int64, term, status, years, genre, tags, studios, person, collection string, unplayed, favorite bool, sortBy string, desc bool, limit, offset int) ([]Movie, int, error) {
	where := []string{}
	if status == "" {
		where = append(where, "status IN ('success','manual')")
	} else {
		where = append(where, "status=?")
	}
	args := []any{}
	if status != "" {
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
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movies WHERE "+cond, args...).Scan(&total); err != nil {
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
	rows, err := s.db.Query("SELECT "+movieCols+" FROM movies WHERE "+cond+" ORDER BY "+column+" "+direction+",id LIMIT ? OFFSET ?", args...)
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
func (s *Store) MovieByPath(path string) (Movie, error) {
	row, err := s.db.Query("SELECT "+movieCols+" FROM movies WHERE source_path=?", path)
	if err != nil {
		return Movie{}, err
	}
	defer row.Close()
	if !row.Next() {
		return Movie{}, sql.ErrNoRows
	}
	return movieScan(row)
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
func (s *Store) SaveData(id int64, d UserData) error {
	played := 0
	if d.Played {
		played = 1
	}
	favorite := 0
	if d.IsFavorite {
		favorite = 1
	}
	hidden := 0
	if d.HideFromResume {
		hidden = 1
	}
	_, err := s.db.Exec(`INSERT INTO userdata(movie_id,position_ticks,play_count,played,last_played_at,last_stopped_ticks,is_favorite,likes,hide_from_resume)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(movie_id) DO UPDATE SET position_ticks=excluded.position_ticks,play_count=excluded.play_count,played=excluded.played,
		last_played_at=excluded.last_played_at,last_stopped_ticks=excluded.last_stopped_ticks,
		is_favorite=excluded.is_favorite,likes=excluded.likes,hide_from_resume=excluded.hide_from_resume`, id, d.PositionTicks, d.PlayCount, played, d.LastPlayedAt, d.StoppedTicks, favorite, d.Likes, hidden)
	return err
}
func (s *Store) SetPlayed(id int64, played bool) error {
	d, _ := s.Data(id)
	d.Played = played
	return s.SaveData(id, d)
}

// SetFavorite 收藏/取消收藏。
func (s *Store) SetFavorite(id int64, favorite bool) error {
	d, _ := s.Data(id)
	d.IsFavorite = favorite
	return s.SaveData(id, d)
}

// SetLikes 个人评分：1 赞 / -1 踩 / 0 清除。
func (s *Store) SetLikes(id int64, likes int) error {
	d, _ := s.Data(id)
	d.Likes = likes
	return s.SaveData(id, d)
}

// SetHideFromResume 隐藏/恢复续播。
func (s *Store) SetHideFromResume(id int64, hidden bool) error {
	d, _ := s.Data(id)
	d.HideFromResume = hidden
	return s.SaveData(id, d)
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
func (s *Store) ReplaceActors(movieID int64, names []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM movie_actors WHERE movie_id=?", movieID); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err = tx.Exec("INSERT INTO actors(name,updated_at) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at", name, time.Now().UTC().Format(time.RFC3339)); err != nil {
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
func (s *Store) Actors(movieID int64) ([]string, error) {
	rows, err := s.db.Query("SELECT actor_name FROM movie_actors WHERE movie_id=? ORDER BY actor_name", movieID)
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

// AllActors 一次返回全部可见影片的演员映射（movie_id → 演员名），供相似度批量打分。
func (s *Store) AllActors() (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT ma.movie_id, ma.actor_name FROM movie_actors ma
		JOIN movies ON movies.id=ma.movie_id
		WHERE movies.status IN ('success','manual')
		ORDER BY ma.actor_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var movieID int64
		var name string
		if err := rows.Scan(&movieID, &name); err != nil {
			return nil, err
		}
		out[movieID] = append(out[movieID], name)
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

func (s *Store) MoviesByStatus(status string) ([]Movie, error) {
	rows, err := s.db.Query("SELECT "+movieCols+" FROM movies WHERE status=? ORDER BY id", status)
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

// RepresentativePoster 返回某媒体库最近入库且带海报的影片海报路径（用作库封面）。
// 无海报时返回空串与 nil 错误。
func (s *Store) RepresentativePoster(libraryID int64) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT poster_path FROM movies
		WHERE library_id=? AND status IN ('success','manual') AND poster_path<>''
		ORDER BY id DESC LIMIT 1`, libraryID).Scan(&path)
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
