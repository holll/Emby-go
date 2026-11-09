package stabilityrehearsal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"emby-go/internal/backup"
)

// This rehearses an offline, same-version schema20 matched restore. It is not
// an old-binary downgrade, nor evidence of atomic DB/NFO snapshots while live.
func TestSchema20MatchedSnapshotRestore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "restored.sqlite")
	lib := filepath.Join(root, "media")
	if err := os.Mkdir(lib, 0700); err != nil {
		t.Fatal(err)
	}
	copyFile := func(t *testing.T, src, dst string) {
		t.Helper()
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// legacyFixture registers its SQL handle with Cleanup. Let that subtest
	// finish before opening the copied fixture, so no historical handle stays
	// alive during the later offline backup or replacement.
	if !t.Run("copy-legacy-fixture", func(t *testing.T) {
		oldPath, oldLib := legacyFixture(t)
		copyFile(t, oldPath, path)
		for _, name := range []string{"A.strm", "A.nfo"} {
			copyFile(t, filepath.Join(oldLib, name), filepath.Join(lib, name))
		}
	}) {
		return
	}

	nfoPath := filepath.Join(lib, "A.nfo")
	originalNFO := []byte("<movie><title>Legacy A</title><tag>manual-keep</tag><tag>中文字幕</tag><actor><name>Manual Actor</name></actor></movie>\n")
	if err := os.WriteFile(nfoPath, originalNFO, 0600); err != nil {
		t.Fatal(err)
	}
	artDir := filepath.Join(lib, "extrafanart")
	if err := os.Mkdir(artDir, 0700); err != nil {
		t.Fatal(err)
	}
	originalAssets := make(map[string][]byte)
	for i, name := range []string{"fanart2.jpg", "fanart10.jpg"} {
		img := image.NewRGBA(image.Rect(0, 0, 16, 12))
		for y := 0; y < 12; y++ {
			for x := 0; x < 16; x++ {
				img.SetRGBA(x, y, color.RGBA{R: uint8(50 + i*150), G: 80, B: 120, A: 255})
			}
		}
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, img, nil); err != nil {
			t.Fatal(err)
		}
		originalAssets[name] = bytes.Clone(encoded.Bytes())
		if err := os.WriteFile(filepath.Join(artDir, name), encoded.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	backdrops := []string{filepath.Join(artDir, "fanart2.jpg"), filepath.Join(artDir, "fanart10.jpg")}
	backdropJSON, err := json.Marshal(backdrops)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit ordered business snapshots are compared before Store.Open or a
	// rescan can repair/reinterpret anything. No byte-identity claim for SQLite.
	queries := map[string]string{
		"schema":    `SELECT json_group_array(json_array(version,checksum)) FROM (SELECT * FROM schema_migrations ORDER BY version)`,
		"users":     `SELECT json_group_array(json_array(id,name,password_hash,is_admin,auth_generation,created_at,updated_at)) FROM (SELECT * FROM users ORDER BY id)`,
		"libraries": `SELECT json_group_array(json_array(id,name,path,sort_order)) FROM (SELECT * FROM libraries ORDER BY id)`,
		"grants":    `SELECT json_group_array(json_array(user_id,library_id)) FROM (SELECT * FROM user_libraries ORDER BY user_id,library_id)`,
		"movies":    `SELECT json_group_array(json_array(id,library_id,source_path,nfo_path,output_dir,title,tags,auto_marks,extra_backdrops,last_scrape_error)) FROM (SELECT * FROM movies ORDER BY id)`,
		"userdata":  `SELECT json_group_array(json_array(user_id,movie_id,position_ticks,play_count,played,last_played_at,last_stopped_ticks,is_favorite,likes,hide_from_resume)) FROM (SELECT * FROM userdata ORDER BY user_id,movie_id)`,
		"sessions":  `SELECT json_group_array(json_array(user_id,session_key,movie_id,started_at,position_ticks)) FROM (SELECT * FROM playback_sessions ORDER BY user_id,session_key)`,
		"events":    `SELECT json_group_array(json_array(user_id,session_key,seq,kind,position_ticks,created_at)) FROM (SELECT * FROM playback_events ORDER BY user_id,session_key,seq)`,
	}
	readBusiness := func(t *testing.T, db *sql.DB) map[string]string {
		t.Helper()
		values := make(map[string]string, len(queries))
		for name, query := range queries {
			var value string
			if err := db.QueryRow(query).Scan(&value); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			values[name] = value
		}
		return values
	}
	var before map[string]string
	if !t.Run("prepare-schema20-and-stop", func(t *testing.T) {
		db := rawDB(t, path)
		mustExec(t, db, "UPDATE libraries SET path=? WHERE id=11", lib)
		mustExec(t, db, "UPDATE movies SET source_path=?,nfo_path=?,output_dir=? WHERE id=42", filepath.Join(lib, "A.strm"), nfoPath, lib)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		s := openStore(t, path)
		retained(t, s)
		if err := s.SetAutoMarks(42, []string{"中文字幕"}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		db = rawDB(t, path)
		mustExec(t, db, `UPDATE movies SET tags='["manual-keep","中文字幕"]',extra_backdrops=?,last_scrape_error='retained-warning' WHERE id=42`, string(backdropJSON))
		if scalar(t, db, "SELECT max(version) FROM schema_migrations") != 22 {
			t.Fatal("snapshot is not schema22")
		}
		before = readBusiness(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	// Every Store/SQL handle above is closed. Files remain quiescent throughout
	// backup.Run; its sequential DB/NFO/artwork copies are not a live transaction.
	result, err := backup.Run(backup.Options{
		DBPath: path, OutDir: filepath.Join(root, "backups"),
		NFORoot: true, ExtraBackdrops: true, Now: time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NFOCount != 1 || result.ExtraBackdropCount != 2 {
		t.Fatalf("incomplete matching set: %+v", result)
	}
	if !t.Run("later-business-writes-and-stop", func(t *testing.T) {
		db := rawDB(t, path)
		mustExec(t, db, `UPDATE users SET name='changed-viewer',password_hash='changed-hash',auth_generation=auth_generation+1 WHERE id=7;
DELETE FROM user_libraries WHERE user_id=7;
INSERT INTO user_libraries(user_id,library_id) VALUES(9,11);
UPDATE userdata SET position_ticks=8888888,play_count=99 WHERE user_id=7;
UPDATE playback_sessions SET position_ticks=8888888 WHERE user_id=7;
UPDATE playback_events SET position_ticks=8888888 WHERE user_id=7;
UPDATE movies SET title='Changed after snapshot',tags='["manual-keep","later-owned"]',auto_marks='["later-owned"]',extra_backdrops='[]',last_scrape_error='later-warning' WHERE id=42`)
		after := readBusiness(t, db)
		for _, name := range []string{"users", "grants", "movies", "userdata", "sessions", "events"} {
			if after[name] == before[name] {
				t.Fatalf("mutation did not change %s", name)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	put(t, nfoPath, "<movie><title>Changed after snapshot</title><tag>later-owned</tag></movie>")
	if err := os.WriteFile(backdrops[0], originalAssets["fanart10.jpg"], 0600); err != nil {
		t.Fatal(err)
	}
	// Both targets resolve directly below this test's private media directory.
	if filepath.Dir(backdrops[1]) != artDir || filepath.Base(backdrops[1]) != "fanart10.jpg" {
		t.Fatal("unexpected deleted artwork path")
	}
	if err := os.Remove(backdrops[1]); err != nil {
		t.Fatal(err)
	}
	newAsset := filepath.Join(artDir, "fanart20.jpg")
	if err := os.WriteFile(newAsset, originalAssets["fanart2.jpg"], 0600); err != nil {
		t.Fatal(err)
	}

	// Offline replacement at the original DB/media paths, not opening a second
	// DB alongside unchanged live media. Restore from the actual backup files.
	copyFile(t, result.Database, path)
	copyFile(t, filepath.Join(result.Dir, "nfo", "lib-11", "A.nfo"), nfoPath)
	for name := range originalAssets {
		copyFile(t, filepath.Join(result.Dir, "extra-backdrops", "lib-11", "extrafanart", name), filepath.Join(artDir, name))
	}
	// Merge-copy alone is not rollback: explicitly remove the post-snapshot
	// image before any scan could discover it and change the restored index.
	if filepath.Dir(newAsset) != artDir || filepath.Base(newAsset) != "fanart20.jpg" {
		t.Fatal("unexpected post-snapshot artwork path")
	}
	if err := os.Remove(newAsset); err != nil {
		t.Fatal(err)
	}
	if !t.Run("raw-restore-before-open-or-scan", func(t *testing.T) {
		db := rawDB(t, path)
		if got := readBusiness(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("business state did not return to matching snapshot:\ngot=%v\nwant=%v", got, before)
		}
		body, err := os.ReadFile(nfoPath)
		if err != nil || !bytes.Equal(body, originalNFO) {
			t.Fatalf("manual NFO restore: %q %v", body, err)
		}
		for name, want := range originalAssets {
			body, err := os.ReadFile(filepath.Join(artDir, name))
			if err != nil || !bytes.Equal(body, want) {
				t.Fatalf("artwork %s differs from snapshot: %v", name, err)
			}
			if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
				t.Fatalf("restored artwork %s is not JPEG: %v", name, err)
			}
		}
		entries, err := os.ReadDir(artDir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if !slices.Equal(names, []string{"fanart10.jpg", "fanart2.jpg"}) {
			t.Fatalf("restore merged post-snapshot artwork: %v", names)
		}
		if _, err := os.Stat(newAsset); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("post-snapshot artwork survived: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}) {
		return
	}
	s := openStore(t, path)
	retained(t, s)
	movie, err := s.Movie(42)
	if err != nil || !slices.Equal(movie.AutoMarks, []string{"中文字幕"}) || !slices.Equal(movie.Tags, []string{"manual-keep", "中文字幕"}) || !slices.Equal(movie.ExtraBackdrops, backdrops) {
		t.Fatalf("Store changed restored ownership/assets: %+v %v", movie, err)
	}
}
