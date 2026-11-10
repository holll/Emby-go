//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// Run real binaries sequentially; every process is joined before logs or state
// are inspected. This is not a systemd or an official release-artifact test.
func nativeUpgradeStartStop(t *testing.T, binary, root, config, base, version string, checks ...func()) string {
	t.Helper()
	var logs bytes.Buffer
	cmd := exec.Command(binary, "-c", config)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	joined := false
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			joined = true
			t.Fatalf("%s startup exited: %v\n%s", version, err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s readiness timed out", version)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var serverID string
	for _, path := range []string{"/api/version", "/System/Info/Public"} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d error=%v", path, resp.StatusCode, err)
		}
		if path == "/api/version" && body["version"] != version {
			t.Fatalf("live version=%v, want %s", body["version"], version)
		}
		if path == "/System/Info/Public" {
			serverID, _ = body["Id"].(string)
			if serverID == "" {
				t.Fatal("empty public server identity")
			}
		}
	}
	for _, check := range checks {
		check()
	}
	started := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatalf("%s shutdown: %v\n%s", version, err, logs.String())
		}
	case <-time.After(17 * time.Second):
		t.Fatal("SIGTERM exceeded shared shutdown budget plus test slack")
	}
	if !strings.Contains(logs.String(), "已退出") || strings.Contains(logs.String(), "强制退出") {
		t.Fatalf("%s did not shut down gracefully: %s", version, logs.String())
	}
	t.Logf("%s healthy and SIGTERM joined in %s", version, time.Since(started))
	return serverID
}

// Raw SQLite deliberately avoids current Store.Open, which would migrate the
// old fixture before the new executable is actually started.
func nativeUpgradeSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Compare every legacy movie column, excluding only the two approved
	// migration additions, whose initial values are asserted after upgrade.
	var movieColumns string
	if err := db.QueryRow(`SELECT group_concat('"' || replace(name, '"', '""') || '"', ',') FROM
		(SELECT name FROM pragma_table_info('movies') WHERE name NOT IN ('extra_backdrops','auto_marks') ORDER BY cid)`).Scan(&movieColumns); err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{
		"libraries":     "SELECT * FROM libraries ORDER BY id",
		"movies":        "SELECT " + movieColumns + " FROM movies ORDER BY id",
		"users":         "SELECT * FROM users ORDER BY id",
		"tokens":        "SELECT * FROM access_tokens ORDER BY token",
		"api_keys":      "SELECT * FROM api_keys ORDER BY key",
		"login_events":  "SELECT * FROM login_events ORDER BY id",
		"authorization": "SELECT * FROM user_libraries ORDER BY user_id,library_id",
		"userdata":      "SELECT * FROM userdata ORDER BY user_id,movie_id",
		"sessions":      "SELECT * FROM playback_sessions ORDER BY user_id,session_key",
		"events":        "SELECT * FROM playback_events ORDER BY id",
		"settings":      "SELECT * FROM settings ORDER BY key",
		"versions":      "SELECT * FROM kv ORDER BY key",
	}
	result := make(map[string]string, len(queries))
	for name, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			records = append(records, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		encoded, err := json.Marshal(struct {
			Columns []string
			Rows    [][]any
		}{columns, records})
		if err != nil {
			t.Fatal(err)
		}
		result[name] = string(encoded)
	}
	return result
}

func TestNativeBinaryUpgradeAndMatchingRestore(t *testing.T) {
	previous := os.Getenv("EMBY_GO_PREVIOUS_BINARY")
	if previous == "" {
		t.Skip("set EMBY_GO_PREVIOUS_BINARY to the frozen v2.2.6 local-tag build; this skip is not upgrade evidence")
	}
	nativeBinaryUpgradeAndMatchingRestore(t, previous, "v2.2.6-local-rehearsal", "Emby-go v2.2.6-local-rehearsal (commit 234a220c9febfba4b1d610fd764448609a31e325, built local-rehearsal)\n", "")
}

func TestNativeReleasedBinaryUpgradeAndMatchingRestore(t *testing.T) {
	previous := os.Getenv("EMBY_GO_PREVIOUS_RELEASE_BINARY")
	if previous == "" {
		t.Skip("set EMBY_GO_PREVIOUS_RELEASE_BINARY to the v2.2.6 Linux amd64 GitHub release asset; this skip is not release upgrade evidence")
	}
	// GitHub release asset 701031564, not a locally rebuilt tag. Check the
	// published asset digest before executing any supplied binary.
	nativeBinaryUpgradeAndMatchingRestore(t, previous, "v2.2.6-234a220", "Emby-go v2.2.6-234a220 (commit 234a220c9febfba4b1d610fd764448609a31e325, built 2026-11-02T08:19:36Z)\n", "7ab1e33307186c660ecfd5f154594293d7079f9f864314cc32274d14b522a758")
}

func nativeBinaryUpgradeAndMatchingRestore(t *testing.T, previous, oldVersion, oldIdentity, oldSHA string) {
	t.Helper()
	previous, err := filepath.Abs(previous)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); oldSHA != "" && got != oldSHA {
		t.Fatalf("previous release SHA256=%s want=%s (refusing execution)", got, oldSHA)
	}
	version, err := exec.Command(previous, "-version").CombinedOutput()
	if err != nil || string(version) != oldIdentity {
		t.Fatalf("previous binary has wrong frozen identity: %q %v", version, err)
	}
	root := t.TempDir()
	current := filepath.Join(root, "emby-go-current")
	currentFixture := buildNativeReleaseFixture(t, current)
	for _, binary := range []string{previous, current} {
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		version, err := exec.Command(binary, "-version").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		provenance := "local source build or supplied pinned candidate; NOT official release evidence"
		if binary == previous && oldSHA != "" {
			provenance = "downloaded GitHub release asset 701031564; pinned digest verified"
		}
		t.Logf("binary SHA256=%x version=%s (%s)", sha256.Sum256(data), strings.TrimSpace(string(version)), provenance)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	dbPath := filepath.Join(root, "data.db")
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("port: %d\ndb_path: %q\nredis_addr: ''\nsetup_code: upgrade-fixture\n", port, dbPath)), 0600); err != nil {
		t.Fatal(err)
	}
	serverID := nativeUpgradeStartStop(t, previous, root, configPath, base, oldVersion)
	media := filepath.Join(root, "media")
	private := filepath.Join(root, "private")
	for _, path := range []string{media, private} {
		if err := os.Mkdir(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(media, "ABC-123.strm")
	nfoPath := filepath.Join(media, "ABC-123.nfo")
	posterPath := filepath.Join(media, "poster.jpg")
	var poster bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 8, 12))
	canvas.Set(1, 1, color.RGBA{R: 210, G: 80, B: 35, A: 255})
	if err := jpeg.Encode(&poster, canvas, nil); err != nil {
		t.Fatal(err)
	}
	assets := map[string][]byte{
		source:                                []byte("https://example.invalid/original.mp4\n"),
		nfoPath:                               []byte("<movie><title>Original</title><tag>手工</tag><tag>中文字幕</tag></movie>\n"),
		posterPath:                            poster.Bytes(),
		filepath.Join(private, "hidden.strm"): []byte("https://example.invalid/hidden.mp4\n"),
		filepath.Join(private, "hidden.nfo"):  []byte("<movie><title>Hidden</title></movie>\n"),
	}
	// Existing disk artwork predates schema17 and must survive without being
	// retroactively claimed by the new extra_backdrops index.
	for _, lib := range []string{media, private} {
		if err := os.Mkdir(filepath.Join(lib, "extrafanart"), 0750); err != nil {
			t.Fatal(err)
		}
	}
	canvas.Set(2, 2, color.RGBA{R: 25, G: 190, B: 90, A: 255})
	var privateBackdrop bytes.Buffer
	if err := jpeg.Encode(&privateBackdrop, canvas, nil); err != nil {
		t.Fatal(err)
	}
	backdropPath := filepath.Join(media, "extrafanart", "fanart2.jpg")
	privateBackdropPath := filepath.Join(private, "extrafanart", "fanart1.jpg")
	assets[backdropPath] = poster.Bytes()
	assets[privateBackdropPath] = privateBackdrop.Bytes()
	for path, data := range assets {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO libraries(id,name,path) VALUES(1,'Fixture',?)", []any{media}},
		{"INSERT INTO movies(id,library_id,source_path,source_protocol,status,nfo_path,title,tags,poster_path,created_at,updated_at) VALUES(1,1,?,'https','success',?,'Original',?,?,'2026-01-01','2026-01-01')", []any{source, nfoPath, "[\"手工\",\"中文字幕\"]", posterPath}},
		{`UPDATE movies SET file_size=987654321,file_mtime='2025-12-31T23:59:59Z',source_container='mp4',
			number='ABC-123',output_dir=?,original_title='Original legacy title',plot='Legacy plot',year=2025,
			premiered='2025-12-30',rating=8.25,director='Fixture director',series='Fixture series',maker='Fixture maker',
			label='Fixture label',collection='Fixture collection',official_rating='R',sortname='Fixture sort',
			taglines='Fixture tagline',provider_id='JavBus:ABC-123',genres='["Drama"]',studios='["Fixture studio"]',
			backdrop_path=?,landscape_path=?,runtime_seconds=4321 WHERE id=1`, []any{media, backdropPath, posterPath}},
		{"INSERT INTO users(id,name,password_hash,is_admin,created_at,updated_at) VALUES(1,'fixture',?,1,'2026-01-01','2026-01-01')", []any{string(hash)}},
		{"INSERT INTO user_libraries(user_id,library_id) VALUES(1,1)", nil},
		{"INSERT INTO userdata(user_id,movie_id,position_ticks,play_count,is_favorite) VALUES(1,1,123456,3,1)", nil},
		{"INSERT INTO playback_sessions(user_id,session_key,movie_id,started_at,last_event_seq,position_ticks,stopped) VALUES(1,'fixture-session',1,'2026-01-01',2,123456,1)", nil},
		{"INSERT INTO playback_events(user_id,session_key,seq,kind,position_ticks,created_at) VALUES(1,'fixture-session',2,'Stopped',123456,'2026-01-01')", nil},
		{"INSERT INTO settings(key,value) VALUES('native_upgrade_fixture','retained')", nil},
		{"INSERT INTO libraries(id,name,path) VALUES(2,'Private',?)", []any{private}},
		{"INSERT INTO movies(id,library_id,source_path,source_protocol,status,nfo_path,title,created_at,updated_at) VALUES(2,2,?,'https','success',?,'Hidden','2026-01-01','2026-01-01')", []any{filepath.Join(private, "hidden.strm"), filepath.Join(private, "hidden.nfo")}},
		{"INSERT INTO users(id,name,password_hash,is_admin,created_at,updated_at,auth_generation) VALUES(2,'viewer',?,0,'2026-01-01','2026-01-01',3)", []any{string(hash)}},
		{"INSERT INTO user_libraries(user_id,library_id) VALUES(2,1)", nil},
		{"INSERT INTO userdata(user_id,movie_id,position_ticks,play_count,is_favorite) VALUES(2,1,654321,2,1)", nil},
		{"INSERT INTO access_tokens(token,created_at,user_id,expires_at,auth_generation) VALUES(?,'2026-01-01',2,NULL,3)", []any{fmt.Sprintf("%x", sha256.Sum256([]byte(nativeUpgradeToken)))}},
		{"INSERT INTO api_keys(key,key_hash,key_prefix,name,created_at,user_id,stable_id,version,rotated_at) VALUES(?,?,?,'fixture-key','2026-01-01',2,'0123456789abcdef0123456789abcdef',4,'2026-01-02T03:04:05Z')", []any{fmt.Sprintf("%x", sha256.Sum256([]byte(nativeUpgradeAPIKey))), fmt.Sprintf("%x", sha256.Sum256([]byte(nativeUpgradeAPIKey))), nativeUpgradeAPIKey[:12]}},
		{"INSERT INTO login_events(id,user_id,name,ip,success,created_at) VALUES(101,1,'fixture','192.0.2.10',1,'2026-01-01T01:02:03Z')", nil},
		{"INSERT INTO login_events(id,user_id,name,ip,success,created_at) VALUES(102,2,'viewer','2001:db8::2',1,'2026-01-02T03:04:05Z')", nil},
		{"INSERT INTO login_events(id,user_id,name,ip,success,created_at) VALUES(103,NULL,'unknown-user','198.51.100.20',0,'2026-01-03T04:05:06Z')", nil},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed old DB: %v", err)
		}
	}
	var schema int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&schema); err != nil || schema != 16 {
		t.Fatalf("old schema=%d, error=%v", schema, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	baseline := nativeUpgradeSnapshot(t, dbPath)
	verifyAPI := func() { assertNativeUpgradeAPI(t, base) }
	if id := nativeUpgradeStartStop(t, previous, root, configPath, base, oldVersion, verifyAPI); id != serverID {
		t.Fatalf("seeded old identity=%q want=%q", id, serverID)
	}
	if got := nativeUpgradeSnapshot(t, dbPath); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("seeded old API verification changed business/auth/history rows:\nwant=%v\ngot=%v", baseline, got)
	}
	// Preserve the stopped old database and matching files directly in the fixture.
	// This tests binary/schema rollback, not a removed archive command.
	bundle := make(map[string][]byte, len(assets)+2)
	for path, data := range assets {
		bundle[path] = data
	}
	for _, path := range []string{dbPath, configPath} {
		bundle[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	for run := range 2 {
		if id := nativeUpgradeStartStop(t, current, root, configPath, base, currentFixture.Version, verifyAPI); id != serverID {
			t.Fatalf("server identity changed after upgrade/restart %d: %q -> %q", run, serverID, id)
		}
		if got := nativeUpgradeSnapshot(t, dbPath); !reflect.DeepEqual(got, baseline) {
			t.Fatalf("business state changed after upgrade/restart %d: got=%v want=%v", run, got, baseline)
		}
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var extras, marks string
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&schema); err != nil || schema != 22 {
		t.Fatalf("upgraded schema=%d error=%v", schema, err)
	}
	if err := db.QueryRow("SELECT extra_backdrops,auto_marks FROM movies WHERE id=1").Scan(&extras, &marks); err != nil || extras != "[]" || marks != "[]" {
		t.Fatalf("migration must not claim old assets/tags: extras=%q marks=%q error=%v", extras, marks, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for path, data := range bundle {
		if path == dbPath {
			continue
		} // SQLite page layout can legitimately change.
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("startup changed bundle asset %s: %v", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	refusal := exec.CommandContext(ctx, previous, "-c", configPath)
	refusal.Dir = root
	output, err := refusal.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "unsupported or modified schema migration 17") {
		t.Fatalf("old binary did not reject upgraded DB at migration boundary: error=%v timeout=%v\n%s", err, ctx.Err(), output)
	}
	if got := nativeUpgradeSnapshot(t, dbPath); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("refused old startup changed business state: got=%v want=%v", got, baseline)
	}
	// These legacy columns were absent from the old ten-column snapshot.
	// Prove they are now observed, and that matching restore discards these
	// deliberate post-snapshot writes rather than just accepting the old binary.
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE movies SET original_title='Post-snapshot change',runtime_seconds=999 WHERE id=1"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(nativeUpgradeSnapshot(t, dbPath), baseline) {
		t.Fatal("snapshot missed post-snapshot legacy movie mutations")
	}
	// Simulate post-snapshot file/config writes as well.
	extraPath := filepath.Join(media, "extrafanart", "fanart99.jpg")
	if err := os.WriteFile(extraPath, poster.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, nfoPath, posterPath, configPath, backdropPath} {
		if err := os.WriteFile(path, []byte("post-snapshot mutation"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// All deletion targets are literal children of this test's verified temp root.
	for _, path := range []string{extraPath, privateBackdropPath, dbPath + "-wal", dbPath + "-shm"} {
		if rel, err := filepath.Rel(root, path); err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("restore deletion escaped fixture root: %s", path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for path, data := range bundle {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("restore mismatch for %s: %v", path, err)
		}
	}
	if _, err := os.Stat(extraPath); !os.IsNotExist(err) {
		t.Fatalf("post-snapshot resource remains: %v", err)
	}
	if got := nativeUpgradeSnapshot(t, dbPath); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("restored business state=%v want=%v", got, baseline)
	}
	if id := nativeUpgradeStartStop(t, previous, root, configPath, base, oldVersion, verifyAPI); id != serverID {
		t.Fatalf("restored identity=%q want=%q", id, serverID)
	}
	if got := nativeUpgradeSnapshot(t, dbPath); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("restored old startup changed business state: %v", got)
	}
	for path, data := range bundle {
		if path == dbPath {
			continue // Only restore itself requires identical SQLite bytes.
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("restored old startup changed bundle asset %s: %v", path, err)
		}
	}
}
