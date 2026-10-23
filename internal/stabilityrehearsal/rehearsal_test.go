// Package stabilityrehearsal exercises public boundaries without production hooks.
package stabilityrehearsal

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/backup"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// Frozen historical DDL, not a current schema with its version lowered.
// Migrations 4 and 12 run with empty credential tables; old-secret conversion
// branches are not represented. No claim is made about every historical DB.
//
//go:embed schema13.sql
var schema13 string

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("SQL %s: %v", query, err)
	}
}
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func integrity(t *testing.T, path string) {
	t.Helper()
	db := rawDB(t, path)
	rows, err := db.Query("PRAGMA integrity_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var verdict string
		if err := rows.Scan(&verdict); err != nil {
			t.Fatal(err)
		}
		n++
		if verdict != "ok" {
			t.Fatal(verdict)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("integrity rows=%d", n)
	}
	fk, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer fk.Close()
	if fk.Next() {
		t.Fatal("foreign_key_check returned violations")
	}
	if err := fk.Err(); err != nil {
		t.Fatal(err)
	}
}
func scalar(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func legacyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "legacy.sqlite")
	lib := filepath.Join(root, "media")
	if err := os.Mkdir(lib, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(lib, "A.strm"), "http://127.0.0.1:1/a.mp4\n")
	put(t, filepath.Join(lib, "A.nfo"), "<movie><title>Legacy A</title></movie>")
	db := rawDB(t, path)
	mustExec(t, db, schema13)
	hash, err := bcrypt.GenerateFromPassword([]byte("rehearsal-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		id    int
		name  string
		admin int
	}{{1, "admin", 1}, {7, "viewer", 0}, {9, "other", 0}} {
		mustExec(t, db, "INSERT INTO users(id,name,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,?,?)", u.id, u.name, string(hash), u.admin, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	}
	mustExec(t, db, "INSERT INTO libraries(id,name,path,sort_order) VALUES(11,'retained',?,3)", lib)
	// Match the non-NULL column set written by the historical UpsertMovie path.
	mustExec(t, db, `INSERT INTO movies(id,library_id,source_path,file_mtime,source_protocol,source_container,number,status,nfo_path,output_dir,title,original_title,plot,year,premiered,rating,director,series,maker,label,genres,tags,studios,poster_path,backdrop_path,created_at,updated_at) VALUES(42,11,?,'t0','http','mp4','','success',?,?,'Legacy A','','',2026,'',0,'','','','','[]','[]','[]','','','t0','t0')`, filepath.Join(lib, "A.strm"), filepath.Join(lib, "A.nfo"), lib)
	mustExec(t, db, `INSERT INTO user_libraries VALUES(7,11);
 INSERT INTO userdata(user_id,movie_id,position_ticks,play_count,played,last_played_at,last_stopped_ticks,is_favorite,likes,hide_from_resume) VALUES(7,42,1234567,4,1,'2026-01-02T00:00:00Z',1234000,1,1,1),(9,42,999,2,0,'2026-01-03T00:00:00Z',998,0,-1,0);
 INSERT INTO playback_sessions(user_id,session_key,movie_id,started_at,position_ticks) VALUES(7,'shared-session',42,'t0',1234567),(9,'shared-session',42,'t0',999);
 INSERT INTO playback_events(user_id,session_key,seq,kind,position_ticks,created_at) VALUES(7,'shared-session',1,'progress',1234567,'t0'),(9,'shared-session',1,'progress',999,'t0');
 INSERT INTO admin_audit_events(user_id,name,method,path,status,created_at) VALUES(500,'deleted','POST','/fixture',200,'t0');`)
	if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 13 {
		t.Fatal("not v13")
	}
	if scalar(t, db, "SELECT count(*) FROM pragma_table_info('users') WHERE name='auth_generation'") != 0 {
		t.Fatal("fixture already has v14 column")
	}
	if scalar(t, db, "SELECT count(*) FROM sqlite_master WHERE name IN ('scan_groups','scan_group_sources')") != 0 {
		t.Fatal("fixture already has v16 scan tables")
	}
	return path, lib
}
func retained(t *testing.T, s *store.Store) {
	t.Helper()
	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 || users[0].ID != 1 || users[1].ID != 7 || users[2].ID != 9 || !users[0].IsAdmin || users[1].IsAdmin || users[2].IsAdmin {
		t.Fatalf("users=%+v", users)
	}
	for _, name := range []string{"admin", "viewer", "other"} {
		if _, ok, err := s.AuthenticateUser(name, "rehearsal-password"); err != nil || !ok {
			t.Fatalf("auth %s: %v %v", name, ok, err)
		}
	}
	grants, err := s.UserLibraryIDs(7)
	if err != nil || !reflect.DeepEqual(grants, []int64{11}) {
		t.Fatalf("grants=%v err=%v", grants, err)
	}
	other, err := s.UserLibraryIDs(9)
	if err != nil || len(other) != 0 {
		t.Fatalf("other grants=%v err=%v", other, err)
	}
	m, err := s.Movie(42)
	if err != nil || m.ID != 42 || m.Title != "Legacy A" || m.LibraryID != 11 {
		t.Fatalf("media=%+v err=%v", m, err)
	}
	a, err := s.DataUser(7, 42)
	if err != nil {
		t.Fatal(err)
	}
	if a.PositionTicks != 1234567 || a.PlayCount != 4 || !a.Played || a.StoppedTicks != 1234000 || !a.IsFavorite || a.Likes != 1 || !a.HideFromResume || a.LastPlayedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("viewer data=%+v", a)
	}
	b, err := s.DataUser(9, 42)
	if err != nil {
		t.Fatal(err)
	}
	if b.PositionTicks != 999 || b.PlayCount != 2 || b.Played || b.IsFavorite || b.Likes != -1 || b.HideFromResume {
		t.Fatalf("other data=%+v", b)
	}
}

func TestSchema13UpgradeAndBackupRestore(t *testing.T) {
	path, lib := legacyFixture(t)
	for i := 0; i < 2; i++ {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		retained(t, s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db := rawDB(t, path)
	if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 16 || scalar(t, db, "SELECT count(*) FROM schema_migrations") != 16 {
		t.Fatal("migration ledger is not complete v16")
	}
	if scalar(t, db, "SELECT count(*) FROM users WHERE auth_generation=1") != 3 {
		t.Fatal("generation backfill")
	}
	if scalar(t, db, "SELECT count(*) FROM playback_sessions") != 2 || scalar(t, db, "SELECT count(*) FROM playback_events") != 2 {
		t.Fatal("playback history lost")
	}
	integrity(t, path)
	s := openStore(t, path)
	result, err := backup.Run(backup.Options{DBPath: path, OutDir: filepath.Join(t.TempDir(), "backups"), NFORoot: true, Now: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if result.NFOCount != 1 {
		t.Fatalf("backup=%+v", result)
	}
	copied, err := os.ReadFile(filepath.Join(result.Dir, "nfo", "lib-11", "A.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(lib, "A.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copied, original) {
		t.Fatal("NFO backup changed bytes")
	}
	// No public restore API: copy the completed snapshot to an unrelated fresh
	// path, then exercise real startup and read APIs. Media paths stay unchanged.
	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	bytes, err := os.ReadFile(result.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoredPath, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	integrity(t, restoredPath)
	restored := openStore(t, restoredPath)
	retained(t, restored)
	restoredDB := rawDB(t, restoredPath)
	for _, check := range []struct {
		query string
		want  int64
	}{
		{"SELECT count(*) FROM schema_migrations", 16},
		{"SELECT count(*) FROM playback_sessions WHERE session_key='shared-session' AND movie_id=42 AND ((user_id=7 AND position_ticks=1234567) OR (user_id=9 AND position_ticks=999))", 2},
		{"SELECT count(*) FROM playback_events WHERE session_key='shared-session' AND seq=1 AND kind='progress' AND ((user_id=7 AND position_ticks=1234567) OR (user_id=9 AND position_ticks=999))", 2},
		{"SELECT count(*) FROM libraries WHERE id=11 AND name='retained' AND sort_order=3", 1},
	} {
		if got := scalar(t, restoredDB, check.query); got != check.want {
			t.Fatalf("restored %s=%d want %d", check.query, got, check.want)
		}
	}
	if err := s.RenameUser(7, "changed-after-backup"); err != nil {
		t.Fatal(err)
	}
	u, err := restored.UserByID(7)
	if err != nil || u.Name != "viewer" {
		t.Fatalf("restore aliases live DB: %+v %v", u, err)
	}
	created, err := restored.CreateUser("post-restore", "rehearsal-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID <= 500 {
		t.Fatalf("historical identity reused: %d", created.ID)
	}
}

// Characterization, not compatibility success: allowed NULL historical rows can
// migrate successfully but cannot currently be read by Movie. Reported to lead.
func TestLegacyNullableMovieReadBoundary(t *testing.T) {
	path, _ := legacyFixture(t)
	db := rawDB(t, path)
	mustExec(t, db, "UPDATE movies SET file_mtime=NULL WHERE id=42")
	s := openStore(t, path)
	_, err := s.Movie(42)
	if err == nil || !strings.Contains(err.Error(), `converting NULL to string is unsupported`) {
		t.Fatalf("nullable row boundary changed: %v", err)
	}
	integrity(t, path)
}

func TestIncrementalBaselineAndInterruptedRerun(t *testing.T) {
	for _, mode := range []string{"cancel", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			media := filepath.Join(root, "media")
			if err := os.Mkdir(media, 0700); err != nil {
				t.Fatal(err)
			}
			s := openStore(t, filepath.Join(root, "scan.sqlite"))
			lib, err := s.AddLibrary("rehearsal", media)
			if err != nil {
				t.Fatal(err)
			}
			source := func(name string) string { return filepath.Join(media, name+".strm") }
			film := func(name, title string) {
				put(t, source(name), "http://127.0.0.1:1/"+name+".mp4\n")
				put(t, filepath.Join(media, name+".nfo"), "<movie><title>"+title+"</title></movie>")
			}
			for _, name := range []string{"A", "B", "C"} {
				film(name, name)
			}
			scan := func() scanner.Result {
				t.Helper()
				r, err := scanner.ScanWithOptions(context.Background(), s, lib, scanner.ScanOptions{Mode: scanner.ScanModeIncremental}, nil)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			first := scan()
			if first.Added != 3 || first.GroupsDone != 3 || !first.IndexChanged || first.Incomplete {
				t.Fatalf("first baseline=%+v", first)
			}
			ids := map[string]int64{}
			for _, name := range []string{"A", "B", "C"} {
				id, err := s.MovieIDByPath(source(name))
				if err != nil {
					t.Fatal(err)
				}
				ids[name] = id
			}
			if err := s.SavePlaybackForUser(1, ids["A"], 123, 2, "t0", 122); err != nil {
				t.Fatal(err)
			}
			unchanged := func() {
				t.Helper()
				before := s.Version("g:version")
				r := scan()
				if r.UnchangedGroups != 3 || r.Added+r.Updated+r.Deleted != 0 || r.IndexChanged || r.Incomplete || s.Version("g:version") != before {
					t.Fatalf("zero-change=%+v", r)
				}
			}
			unchanged()
			film("A", "A changed")
			film("B", "B changed")
			film("D", "D")
			if err := os.Remove(source("C")); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := false
			r, err := scanner.ScanWithOptions(ctx, s, lib, scanner.ScanOptions{Mode: scanner.ScanModeIncremental}, func(p scanner.Progress) {
				if mode == "cancel" && p.Done == 1 {
					cancel()
				}
				if mode == "read-failure" && p.Done == 0 && !injected {
					injected = true
					if e := os.Remove(source("B")); e != nil {
						t.Fatal(e)
					}
				}
			})
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel err=%v", err)
			}
			if mode == "read-failure" && !errors.Is(err, scanner.ErrScanIncomplete) {
				t.Fatalf("read failure err=%v result=%+v", err, r)
			}
			if !r.Incomplete || r.CleanupPerformed {
				t.Fatalf("interrupted cleanup=%+v", r)
			}
			if id, e := s.MovieIDByPath(source("C")); e != nil || id != ids["C"] {
				t.Fatalf("incomplete scan deleted C: %d %v", id, e)
			}
			if mode == "read-failure" {
				film("B", "B changed")
			}
			retry := scan()
			if retry.Incomplete || !retry.CleanupPerformed || retry.Deleted != 1 {
				t.Fatalf("retry=%+v", retry)
			}
			for _, name := range []string{"A", "B"} {
				id, e := s.MovieIDByPath(source(name))
				if e != nil || id != ids[name] {
					t.Fatalf("%s ID changed %d -> %d (%v)", name, ids[name], id, e)
				}
				m, e := s.Movie(id)
				if e != nil || m.Title != name+" changed" {
					t.Fatalf("metadata=%+v %v", m, e)
				}
			}
			if _, e := s.MovieIDByPath(source("D")); e != nil {
				t.Fatal(e)
			}
			if _, e := s.MovieIDByPath(source("C")); e == nil {
				t.Fatal("retry left deleted media")
			}
			data, e := s.DataUser(1, ids["A"])
			if e != nil || data.PositionTicks != 123 || data.PlayCount != 2 {
				t.Fatalf("watch state after retry=%+v %v", data, e)
			}
			unchanged()
			groups, e := s.LoadScanGroups(lib.ID)
			if e != nil || len(groups) != 3 {
				t.Fatalf("baseline=%v %v", groups, e)
			}
			integrity(t, filepath.Join(root, "scan.sqlite"))
			t.Logf("%s: baseline added=%d; interrupted=%+v; recovered=%+v", mode, first.Added, r, retry)
		})
	}
}
