package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/nfo"
)

func manualEditVersionValues(t *testing.T, app *App, fixture *sql.DB, libraryID int64) [3]int64 {
	t.Helper()
	var result [3]int64
	for i, key := range []string{"g:version", fmt.Sprintf("lib:%d:version", libraryID)} {
		if err := fixture.QueryRow("SELECT value FROM kv WHERE key=?", key).Scan(&result[i]); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	result[2], err = strconv.ParseInt(app.db.Version("g:version"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestManualVersionPartialLocksAndRecovery(t *testing.T) {
	for _, stage := range []string{"global", "library", "update", "commit", "index"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			app, media := detailTestApp(t, root, `<movie><title>Old</title><actor><name>Actor</name></actor><lockedfields>Name|FutureField</lockedfields><custom>keep</custom></movie>`)
			ts, token := detailServer(t, app)
			lib, err := app.db.AddLibrary("L", media)
			if err != nil {
				t.Fatal(err)
			}
			scanOnce(t, ts, token)
			id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/admin/items/" + strconv.FormatInt(id, 10)
			fixture, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)")
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()
			before := manualEditVersionValues(t, app, fixture, lib.ID)
			snapshotFixture := adminDeleteFixture{a: app, db: fixture}
			snapshot := adminDeleteSnapshot(t, snapshotFixture)
			var inject string
			switch stage {
			case "global":
				inject = `CREATE TRIGGER fail_manual_version BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'private-manual-version'); END`
			case "library":
				inject = fmt.Sprintf(`CREATE TRIGGER fail_manual_version BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'private-manual-version'); END`, lib.ID)
			case "update":
				inject = `CREATE TRIGGER fail_manual_version BEFORE UPDATE OF status ON movies BEGIN SELECT RAISE(ABORT,'private-manual-version'); END`
			case "commit":
				inject = `CREATE TABLE fixture_manual_parent(id INTEGER PRIMARY KEY); CREATE TABLE fixture_manual_child(parent_id INTEGER REFERENCES fixture_manual_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_manual_version AFTER UPDATE OF status ON movies BEGIN INSERT INTO fixture_manual_child(parent_id) VALUES(999); END`
			case "index":
				inject = `CREATE TRIGGER fail_manual_version BEFORE INSERT ON movies BEGIN SELECT RAISE(ABORT,'private-manual-version'); END`
			}
			if _, err := fixture.Exec(inject); err != nil {
				t.Fatal(err)
			}
			resp, body := embyCall(t, ts, "PATCH", path, token, `{"locked_fields":["Overview","FutureField"],"lock_data":true}`)
			manual, code := "pending", "MANUAL_MARK_FAILED"
			var delta int64
			if stage == "index" {
				manual, code, delta = "saved", "INDEX_REFRESH_FAILED", 1
			}
			if resp.StatusCode != http.StatusOK || body["status"] != "partial_success" || body["saved"] != true || body["indexed"] != false || body["manual_status"] != manual {
				t.Fatalf("partial=%d %+v", resp.StatusCode, body)
			}
			if body["warnings"].([]any)[0].(map[string]any)["code"] != code {
				t.Fatalf("warning=%+v", body)
			}
			raw, _ := json.Marshal(body)
			if strings.Contains(string(raw), "private-manual-version") || strings.Contains(string(raw), root) {
				t.Fatalf("partial leaked fault: %s", raw)
			}
			meta, err := nfo.Read(filepath.Join(media, "ABF-018.nfo"))
			if err != nil || meta.LockedFields != "Overview|FutureField" || meta.LockData != "true" || meta.Title != "Old" {
				t.Fatalf("saved locks=%+v err=%v", meta, err)
			}
			got := manualEditVersionValues(t, app, fixture, lib.ID)
			for i := range got {
				if got[i] != before[i]+delta {
					t.Fatalf("partial versions=%v before=%v delta=%d", got, before, delta)
				}
			}
			if stage != "index" && adminDeleteSnapshot(t, snapshotFixture) != snapshot {
				t.Fatal("failed mark changed business rows or versions")
			}
			if stage == "index" {
				m, err := app.db.Movie(id)
				if err != nil || m.Status != "manual" || m.Title != "Old" {
					t.Fatalf("committed manual=%+v %v", m, err)
				}
				oldRecovery, _ := json.Marshal(body["recovery"])
				// A new lock-only save on an already-manual row must still publish.
				resp, body = embyCall(t, ts, "PATCH", path, token, `{"locked_fields":[],"lock_data":false}`)
				if resp.StatusCode != http.StatusOK || body["status"] != "partial_success" || body["manual_status"] != "saved" {
					t.Fatalf("repeat=%d %+v", resp.StatusCode, body)
				}
				delta++
				got = manualEditVersionValues(t, app, fixture, lib.ID)
				for i := range got {
					if got[i] != before[i]+delta {
						t.Fatalf("repeated lock save did not publish: %v before=%v", got, before)
					}
				}
				conflict, conflictBody := embyCall(t, ts, "POST", path+"/recover-edit", token, string(oldRecovery))
				if conflict.StatusCode != http.StatusConflict || conflictBody["code"] != "NFO_CHANGED" {
					t.Fatalf("old lock digest=%d %+v", conflict.StatusCode, conflictBody)
				}
			}
			if _, err := fixture.Exec("DROP TRIGGER fail_manual_version"); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(media, "ABF-018.nfo")
			saved, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			recovery, _ := json.Marshal(body["recovery"])
			resp, body = embyCall(t, ts, "POST", path+"/recover-edit", token, string(recovery))
			if resp.StatusCode != http.StatusOK || body["status"] != "success" || body["indexed"] != true {
				t.Fatalf("recovery=%d %+v", resp.StatusCode, body)
			}
			got = manualEditVersionValues(t, app, fixture, lib.ID)
			for i := range got {
				if got[i] != before[i]+delta+2 {
					t.Fatalf("recovery versions=%v before=%v delta=%d", got, before, delta)
				}
			}
			after, err := os.ReadFile(file)
			if err != nil || string(after) != string(saved) {
				t.Fatalf("recovery rewrote NFO: %v", err)
			}
			m, err := app.db.Movie(id)
			if err != nil || m.Status != "manual" || m.Title != "Old" {
				t.Fatalf("recovered movie=%+v %v", m, err)
			}
		})
	}
}
