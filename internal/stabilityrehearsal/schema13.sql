-- Frozen at HEAD 1a77249: actual baseline and migrations 2-13 replayed on an empty DB.
-- Not a user_version downgrade or a claim to represent every historical installation.

CREATE TABLE IF NOT EXISTS libraries (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, path TEXT UNIQUE NOT NULL, type TEXT DEFAULT 'movies', enabled INTEGER DEFAULT 1);
CREATE TABLE IF NOT EXISTS movies (id INTEGER PRIMARY KEY AUTOINCREMENT, library_id INTEGER NOT NULL, source_path TEXT UNIQUE NOT NULL, file_size INTEGER DEFAULT 0, file_mtime TEXT, source_protocol TEXT, source_container TEXT, number TEXT, status TEXT NOT NULL, nfo_path TEXT, output_dir TEXT, title TEXT, original_title TEXT, plot TEXT, year INTEGER, premiered TEXT, rating REAL, director TEXT, series TEXT, maker TEXT, label TEXT, collection TEXT, official_rating TEXT, sortname TEXT, taglines TEXT, provider_id TEXT, genres TEXT, tags TEXT, studios TEXT, poster_path TEXT, backdrop_path TEXT, landscape_path TEXT, runtime_seconds INTEGER DEFAULT 0, additional_parts TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY(library_id) REFERENCES libraries(id));
CREATE TABLE IF NOT EXISTS userdata (movie_id INTEGER PRIMARY KEY REFERENCES movies(id) ON DELETE CASCADE, position_ticks INTEGER DEFAULT 0, play_count INTEGER DEFAULT 0, played INTEGER DEFAULT 0, last_played_at TEXT, last_stopped_ticks INTEGER DEFAULT -1, is_favorite INTEGER NOT NULL DEFAULT 0, likes INTEGER NOT NULL DEFAULT 0, hide_from_resume INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS api_probe (id INTEGER PRIMARY KEY AUTOINCREMENT, method TEXT, path TEXT, query TEXT, body_preview TEXT, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS actors (name TEXT PRIMARY KEY, avatar_url TEXT, avatar_tag TEXT, updated_at TEXT);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS movie_actors (movie_id INTEGER, actor_name TEXT, PRIMARY KEY(movie_id, actor_name), FOREIGN KEY(movie_id) REFERENCES movies(id) ON DELETE CASCADE, FOREIGN KEY(actor_name) REFERENCES actors(name));
CREATE TABLE IF NOT EXISTS movie_features (movie_id INTEGER NOT NULL, kind TEXT NOT NULL, value TEXT NOT NULL, weight INTEGER NOT NULL, PRIMARY KEY(movie_id, kind, value), FOREIGN KEY(movie_id) REFERENCES movies(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '0');
CREATE TABLE IF NOT EXISTS administrators (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS access_tokens (token TEXT PRIMARY KEY, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS api_keys (key TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scheduled_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, type TEXT NOT NULL, cron TEXT NOT NULL, params TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT, last_status TEXT, last_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);;
ALTER TABLE movies ADD COLUMN last_scrape_at TEXT;
ALTER TABLE movies ADD COLUMN last_scrape_error TEXT;
ALTER TABLE libraries ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_movies_library_status ON movies(library_id,status);
CREATE INDEX IF NOT EXISTS idx_movies_status ON movies(status);
CREATE INDEX IF NOT EXISTS idx_movies_collection ON movies(collection);
CREATE INDEX IF NOT EXISTS idx_movie_features_lookup ON movie_features(kind,value,movie_id);
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL
);
INSERT INTO schema_migrations VALUES(1,'9d69f4dc7203e14d0c93bc1bfb9bb2875fa34bde2578c56f2da2c7a62426e100','2026-01-01T00:00:00Z');
-- Migration 2 (empty credentials: hashing migrations have no rows).
CREATE TABLE IF NOT EXISTS playback_sessions (
		session_key TEXT PRIMARY KEY,
		movie_id INTEGER NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
		device_id TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		last_event_seq INTEGER NOT NULL DEFAULT 0,
		position_ticks INTEGER NOT NULL DEFAULT 0,
		stopped INTEGER NOT NULL DEFAULT 0,
		stopped_at TEXT
	);
CREATE TABLE IF NOT EXISTS playback_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_key TEXT NOT NULL REFERENCES playback_sessions(session_key) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		position_ticks INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		UNIQUE(session_key, seq)
	);
CREATE INDEX IF NOT EXISTS idx_playback_sessions_movie ON playback_sessions(movie_id);
CREATE INDEX IF NOT EXISTS idx_playback_events_session ON playback_events(session_key, seq);
INSERT INTO schema_migrations VALUES(2,'cbe9586da2cc5ab2b92fb29e0dcd07f0452f9e3e6e577b31a7de8dc220bdf3b9','2026-01-01T00:00:00Z');
-- Migration 3 (empty credentials: hashing migrations have no rows).
ALTER TABLE access_tokens ADD COLUMN expires_at INTEGER;
UPDATE access_tokens SET expires_at = CAST(strftime('%s', created_at) AS INTEGER) + 2592000 WHERE expires_at IS NULL;
INSERT INTO schema_migrations VALUES(3,'cead16764f66814daac8cea506be3423bfd813049980011beab4db67cc0bd3ff','2026-01-01T00:00:00Z');
-- Migration 4 (empty credentials: hashing migrations have no rows).
ALTER TABLE api_keys ADD COLUMN key_hash TEXT;
ALTER TABLE api_keys ADD COLUMN key_prefix TEXT;
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
INSERT INTO schema_migrations VALUES(4,'fb89a994f66bbdf70992dcb84e67b09f859f543855574eb0e8d030ff75eace85','2026-01-01T00:00:00Z');
-- Migration 5 (empty credentials: hashing migrations have no rows).
CREATE TABLE IF NOT EXISTS edit_failures(
		movie_id INTEGER PRIMARY KEY REFERENCES movies(id) ON DELETE CASCADE,
		code TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 1,
		first_failed_at TEXT NOT NULL,
		last_failed_at TEXT NOT NULL
	);
CREATE INDEX IF NOT EXISTS idx_edit_failures_last ON edit_failures(last_failed_at);
INSERT INTO schema_migrations VALUES(5,'9e077ffa634b59bc43d7676347ff8f66b46333627108dd8be47c887d08e7f8cc','2026-01-01T00:00:00Z');
-- Migration 6 (empty credentials: hashing migrations have no rows).
CREATE INDEX IF NOT EXISTS idx_movie_actors_actor_movie ON movie_actors(actor_name, movie_id);
CREATE INDEX IF NOT EXISTS idx_movies_visible_title ON movies(title, id) WHERE status IN ('success','manual');
CREATE INDEX IF NOT EXISTS idx_movies_visible_library_title ON movies(library_id, title, id) WHERE status IN ('success','manual');
ANALYZE;
INSERT INTO schema_migrations VALUES(6,'3bb138e21bd3fe28fad450762463bac95b2c26329c2534f7d3559b7604f77501','2026-01-01T00:00:00Z');
-- Migration 7 (empty credentials: hashing migrations have no rows).
CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
INSERT OR IGNORE INTO users(id,name,password_hash,is_admin,created_at,updated_at)
	 SELECT id,username,password_hash,1,created_at,created_at FROM administrators WHERE id=1;
ALTER TABLE access_tokens ADD COLUMN user_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE api_keys ADD COLUMN user_id INTEGER NOT NULL DEFAULT 1;
INSERT INTO schema_migrations VALUES(7,'8c1ecf56f4601d83c028a858739687609973f55f139277971c36d65388b1aa91','2026-01-01T00:00:00Z');
-- Migration 8 (empty credentials: hashing migrations have no rows).
CREATE TABLE userdata_new (
		user_id INTEGER NOT NULL DEFAULT 1,
		movie_id INTEGER NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
		position_ticks INTEGER DEFAULT 0,
		play_count INTEGER DEFAULT 0,
		played INTEGER DEFAULT 0,
		last_played_at TEXT,
		last_stopped_ticks INTEGER DEFAULT -1,
		is_favorite INTEGER NOT NULL DEFAULT 0,
		likes INTEGER NOT NULL DEFAULT 0,
		hide_from_resume INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(user_id, movie_id)
	);
INSERT INTO userdata_new(user_id,movie_id,position_ticks,play_count,played,last_played_at,last_stopped_ticks,is_favorite,likes,hide_from_resume)
		SELECT 1,movie_id,position_ticks,play_count,played,last_played_at,last_stopped_ticks,is_favorite,likes,hide_from_resume FROM userdata;
DROP TABLE userdata;
ALTER TABLE userdata_new RENAME TO userdata;
CREATE INDEX IF NOT EXISTS idx_userdata_user_movie ON userdata(user_id, movie_id);
ALTER TABLE playback_sessions ADD COLUMN user_id INTEGER NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_playback_sessions_user_started ON playback_sessions(user_id, started_at);
CREATE TABLE IF NOT EXISTS login_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		name TEXT NOT NULL,
		ip TEXT,
		success INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
CREATE INDEX IF NOT EXISTS idx_login_events_created ON login_events(created_at);
INSERT INTO schema_migrations VALUES(8,'1f62bb2c036f7e1ff81b9ced8035b88d488d98d0e06ccf9d5388172ebebcf1d9','2026-01-01T00:00:00Z');
-- Migration 9 (empty credentials: hashing migrations have no rows).
CREATE TABLE IF NOT EXISTS user_libraries (
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
		PRIMARY KEY(user_id, library_id)
	);
CREATE INDEX IF NOT EXISTS idx_user_libraries_library ON user_libraries(library_id);
INSERT INTO schema_migrations VALUES(9,'6e0ae223b8235340138596d29d209fa5bed78242c62444ae41cf8d0d0c753ec3','2026-01-01T00:00:00Z');
-- Migration 10 (empty credentials: hashing migrations have no rows).
CREATE TABLE playback_events_stage (
		id INTEGER,
		user_id INTEGER,
		session_key TEXT,
		seq INTEGER,
		kind TEXT,
		position_ticks INTEGER,
		created_at TEXT
	);
INSERT INTO playback_events_stage(id,user_id,session_key,seq,kind,position_ticks,created_at)
		SELECT e.id,COALESCE(s.user_id,1),e.session_key,e.seq,e.kind,e.position_ticks,e.created_at
		FROM playback_events e LEFT JOIN playback_sessions s ON s.session_key=e.session_key;
DROP TABLE playback_events;
CREATE TABLE playback_sessions_new (
		user_id INTEGER NOT NULL DEFAULT 1,
		session_key TEXT NOT NULL,
		movie_id INTEGER NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
		device_id TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		last_event_seq INTEGER NOT NULL DEFAULT 0,
		position_ticks INTEGER NOT NULL DEFAULT 0,
		stopped INTEGER NOT NULL DEFAULT 0,
		stopped_at TEXT,
		PRIMARY KEY(user_id, session_key)
	);
INSERT INTO playback_sessions_new(user_id,session_key,movie_id,device_id,started_at,last_event_seq,position_ticks,stopped,stopped_at)
		SELECT COALESCE(user_id,1),session_key,movie_id,device_id,started_at,last_event_seq,position_ticks,stopped,stopped_at FROM playback_sessions;
DROP TABLE playback_sessions;
ALTER TABLE playback_sessions_new RENAME TO playback_sessions;
CREATE INDEX idx_playback_sessions_movie ON playback_sessions(movie_id);
CREATE TABLE playback_events_new (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL DEFAULT 1,
		session_key TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		position_ticks INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		UNIQUE(user_id, session_key, seq),
		FOREIGN KEY(user_id, session_key) REFERENCES playback_sessions(user_id, session_key) ON DELETE CASCADE
	);
INSERT INTO playback_events_new(id,user_id,session_key,seq,kind,position_ticks,created_at)
		SELECT id,COALESCE(user_id,1),session_key,seq,kind,position_ticks,created_at FROM playback_events_stage;
DROP TABLE playback_events_stage;
ALTER TABLE playback_events_new RENAME TO playback_events;
CREATE INDEX idx_playback_events_session ON playback_events(user_id, session_key, seq);
INSERT INTO schema_migrations VALUES(10,'156596257ca4b405b877d28032417511872543f046ee2daee4f5ecaa061b3105','2026-01-01T00:00:00Z');
-- Migration 11 (empty credentials: hashing migrations have no rows).
CREATE INDEX IF NOT EXISTS idx_userdata_movie ON userdata(movie_id);
CREATE INDEX IF NOT EXISTS idx_login_events_user_created ON login_events(user_id, created_at);
ANALYZE;
INSERT INTO schema_migrations VALUES(11,'84055e39fd0d3096f5b1c33166d9f44ffb4f55068e460f4424969673a8a4e33d','2026-01-01T00:00:00Z');
-- Migration 12 (empty credentials: hashing migrations have no rows).
INSERT INTO schema_migrations VALUES(12,'5a5f2d112634a0c9a13fec898c2200236a921a0d792c1e6d755b32c48fe2cff4','2026-01-01T00:00:00Z');
-- Migration 13 (empty credentials: hashing migrations have no rows).
CREATE TABLE IF NOT EXISTS admin_audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		status INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
CREATE INDEX IF NOT EXISTS idx_admin_audit_created ON admin_audit_events(created_at);
INSERT INTO schema_migrations VALUES(13,'2c02cbbd3c7140ce4222182d0abf389f85e497f76b45886ece2ac8d6c887774b','2026-01-01T00:00:00Z');
