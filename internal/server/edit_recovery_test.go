package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/nfo"
)

func TestEditedNFOPartialSuccessAndRecovery(t *testing.T) {
	for _, stage := range []struct{ name, inject, undo, code, manual string }{
		{"manual", `CREATE TRIGGER fail_edit BEFORE UPDATE OF status ON movies BEGIN SELECT RAISE(ABORT,'fixture-private-error'); END`, `DROP TRIGGER fail_edit`, "MANUAL_MARK_FAILED", "pending"},
		{"library", `UPDATE movies SET library_id=99999`, `UPDATE movies SET library_id=1`, "LIBRARY_READ_FAILED", "saved"},
		{"upsert", `CREATE TRIGGER fail_edit BEFORE INSERT ON movies BEGIN SELECT RAISE(ABORT,'fixture-private-error'); END`, `DROP TRIGGER fail_edit`, "INDEX_REFRESH_FAILED", "saved"},
		{"actors", `CREATE TRIGGER fail_edit BEFORE DELETE ON movie_actors BEGIN SELECT RAISE(ABORT,'fixture-private-error'); END`, `DROP TRIGGER fail_edit`, "INDEX_REFRESH_FAILED", "saved"},
		{"manual_version", `CREATE TRIGGER fail_edit BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'fixture-private-error'); END`, `DROP TRIGGER fail_edit`, "MANUAL_MARK_FAILED", "pending"},
		{"index_version", `CREATE TRIGGER fail_edit BEFORE INSERT ON kv WHEN NEW.key='g:version' AND EXISTS(SELECT 1 FROM movies WHERE status='manual') BEGIN SELECT RAISE(ABORT,'fixture-private-error'); END`, `DROP TRIGGER fail_edit`, "INDEX_REFRESH_FAILED", "saved"},
	} {
		t.Run(stage.name, func(t *testing.T) {
			root := t.TempDir()
			app, media := detailTestApp(t, root, `<movie><title>Old</title><actor><name>Actor</name></actor><lockedfields>Name|FutureField</lockedfields><custom>keep</custom></movie>`)
			ts, token := detailServer(t, app)
			if _, err := app.db.AddLibrary("L", media); err != nil {
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
			if _, err := fixture.Exec(stage.inject); err != nil {
				t.Fatal(err)
			}
			resp, body := embyCall(t, ts, "PUT", path, token, `{"title":"Manual"}`)
			if resp.StatusCode != 200 || body["status"] != "partial_success" || body["saved"] != true || body["indexed"] != false || body["manual_status"] != stage.manual {
				t.Fatalf("response: %d %+v", resp.StatusCode, body)
			}
			warnings := body["warnings"].([]any)
			if warnings[0].(map[string]any)["code"] != stage.code {
				t.Fatalf("warning=%+v", body)
			}
			raw, _ := json.Marshal(body)
			for _, secret := range []string{"fixture-private-error", root, "recovery_id", "task_id"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("leak/fiction: %s", raw)
				}
			}
			meta, err := nfo.Read(filepath.Join(media, "ABF-018.nfo"))
			if err != nil || meta.Title != "Manual" {
				t.Fatalf("saved NFO=%+v err=%v", meta, err)
			}
			saved, _ := os.ReadFile(filepath.Join(media, "ABF-018.nfo"))
			recovery, _ := json.Marshal(body["recovery"])
			// The public recovery endpoint is still protected by the real auth middleware.
			unauth, _ := embyCall(t, ts, "POST", path+"/recover-edit", "", string(recovery))
			if unauth.StatusCode != http.StatusUnauthorized {
				t.Fatal(unauth.StatusCode)
			}
			if !app.claimNFO("edit") {
				t.Fatal("write gate leaked")
			}
			busy, _ := embyCall(t, ts, "POST", path+"/recover-edit", token, string(recovery))
			if busy.StatusCode != 409 {
				t.Fatal(busy.StatusCode)
			}
			app.releaseNFO("edit")
			if _, err := fixture.Exec(stage.undo); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				recovered, result := embyCall(t, ts, "POST", path+"/recover-edit", token, string(recovery))
				if recovered.StatusCode != 200 || result["status"] != "success" || result["indexed"] != true {
					t.Fatalf("recovered=%d %+v", recovered.StatusCode, result)
				}
			}
			movie, err := app.db.Movie(id)
			if err != nil || movie.Title != "Manual" || movie.Status != "manual" {
				t.Fatalf("index=%+v err=%v", movie, err)
			}
			after, _ := os.ReadFile(filepath.Join(media, "ABF-018.nfo"))
			if string(saved) != string(after) || !strings.Contains(string(after), "<custom>keep</custom>") || !strings.Contains(string(after), "FutureField") {
				t.Fatalf("recovery rewrote NFO: %s", after)
			}
		})
	}
}

func TestEditedNFORecoveryRefusesChangedOrMissingFile(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), `<movie><title>Old</title></movie>`)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/items/" + strconv.FormatInt(id, 10) + "/recover-edit"
	file := filepath.Join(media, "ABF-018.nfo")
	digest, err := nfoDigest(file)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"nfo_sha256":"` + digest + `"}`
	writeFile(t, file, `<movie><title>External</title><custom>new</custom></movie>`)
	resp, result := embyCall(t, ts, "POST", path, token, body)
	if resp.StatusCode != 409 || result["code"] != "NFO_CHANGED" {
		t.Fatalf("%d %+v", resp.StatusCode, result)
	}
	current, _ := os.ReadFile(file)
	if !strings.Contains(string(current), "External") {
		t.Fatal("overwrote external edit")
	}
	// This exact fixture path was created above; remove only that verified file.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	resp, result = embyCall(t, ts, "POST", path, token, body)
	if resp.StatusCode != 409 || result["code"] != "NFO_UNAVAILABLE" {
		t.Fatalf("%d %+v", resp.StatusCode, result)
	}
	for _, bad := range []string{`{}`, `{"nfo_sha256":"not-a-digest"}`} {
		resp, _ = embyCall(t, ts, "POST", path, token, bad)
		if resp.StatusCode != 400 {
			t.Fatal(resp.StatusCode)
		}
	}
}

func TestEditedNFOUnavailableSourceIsPartialNotDeleted(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), `<movie><title>Old</title></movie>`)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	source := filepath.Join(media, "ABF-018.strm")
	id, err := app.db.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/items/" + strconv.FormatInt(id, 10)
	writeFile(t, source, "ed2k://invalid\n")
	resp, body := embyCall(t, ts, "PUT", path, token, `{"title":"Saved"}`)
	if resp.StatusCode != 200 || body["status"] != "partial_success" || body["indexed"] != false {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
	if _, err := app.db.Movie(id); err != nil {
		t.Fatalf("must not delete edited target: %v", err)
	}
	writeFile(t, source, "https://media.test/new.mp4\n")
	recovery, _ := json.Marshal(body["recovery"])
	resp, body = embyCall(t, ts, "POST", path+"/recover-edit", token, string(recovery))
	if resp.StatusCode != 200 || body["indexed"] != true {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
}

func TestEditedNFOFailureBeforeSaveIsNotSuccess(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), `<movie><title>Old</title></movie>`)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(media, "ABF-018.nfo")
	writeFile(t, file, "<movie><title>broken")
	resp, body := embyCall(t, ts, "PUT", "/api/admin/items/"+strconv.FormatInt(id, 10), token, `{"title":"No"}`)
	if resp.StatusCode < 400 || body["saved"] == true {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != "<movie><title>broken" {
		t.Fatal("changed invalid file")
	}
}
