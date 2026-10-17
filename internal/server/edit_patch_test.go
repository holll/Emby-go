package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/nfo"
)

const patchFixture = `<movie><title>Old</title><num>OLD-001</num><originaltitle>Original</originaltitle><plot>Plot</plot><year>2020</year><genre>Genre</genre><tag>Tag</tag><studio>Studio</studio><maker>Maker</maker><label>Label</label><lockdata>true</lockdata><lockedfields>Name|Genres|FutureField</lockedfields><actor><name>Actor</name></actor><custom>keep</custom><vendor:plot xmlns:vendor="urn:vendor">extension plot</vendor:plot><vendor:title xmlns:vendor="urn:vendor">extension title</vendor:title><vendor:genre xmlns:vendor="urn:vendor">extension genre</vendor:genre><vendor:year xmlns:vendor="urn:vendor">2099</vendor:year></movie>`

func TestManualPatchClearAndLegacyCompatibility(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), patchFixture)
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
	file := filepath.Join(media, "ABF-018.nfo")
	empty := `{"title":"","number":"","original_title":"","plot":"","year":0,"genres":[],"tags":[],"studios":[],"maker":"","label":""}`
	resp, body := embyCall(t, ts, "PUT", path, token, empty)
	if resp.StatusCode != 200 || body["status"] != "success" {
		t.Fatalf("legacy=%d %+v", resp.StatusCode, body)
	}
	meta, err := nfo.Read(file)
	if err != nil || meta.Title != "Old" || meta.Year != 2020 || meta.Plot != "Plot" || len(meta.Genres) != 1 {
		t.Fatalf("legacy changed: %+v %v", meta, err)
	}
	resp, body = embyCall(t, ts, "PATCH", path, token, `{"plot":"Changed"}`)
	if resp.StatusCode != 200 || body["status"] != "success" {
		t.Fatalf("omit=%d %+v", resp.StatusCode, body)
	}
	meta, err = nfo.Read(file)
	if err != nil || meta.Plot != "Changed" || meta.Title != "Old" || meta.Year != 2020 || meta.LockData != "true" || meta.LockedFields != "Name|Genres|FutureField" {
		t.Fatalf("omitted fields changed: %+v %v", meta, err)
	}
	clear := strings.TrimSuffix(empty, "}") + `,"locked_fields":[],"lock_data":false}`
	resp, body = embyCall(t, ts, "PATCH", path, token, clear)
	if resp.StatusCode != 200 || body["status"] != "success" || body["indexed"] != true {
		t.Fatalf("clear=%d %+v", resp.StatusCode, body)
	}
	meta, err = nfo.Read(file)
	if err != nil || meta.Title != "" || meta.Number != "" || meta.OriginalTitle != "" || meta.Plot != "" || meta.Year != 0 || meta.Maker != "" || meta.Label != "" || len(meta.Genres)+len(meta.Tags)+len(meta.Studios) != 0 || meta.LockData != "false" || meta.LockedFields != "" {
		t.Fatalf("not cleared: %+v %v", meta, err)
	}
	movie, err := app.db.Movie(id)
	if err != nil || movie.Title != "" || movie.Number != "" || movie.Plot != "" || movie.Year != 0 || len(movie.Genres)+len(movie.Tags)+len(movie.Studios) != 0 || movie.Status != "manual" {
		t.Fatalf("index=%+v %v", movie, err)
	}
	raw, _ := os.ReadFile(file)
	if !strings.Contains(string(raw), "<custom>keep</custom>") || len(meta.Actors) != 1 {
		t.Fatalf("unrelated content lost: %s", raw)
	}
}

func TestManualPatchRejectsBeforeWriting(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), patchFixture)
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
	file := filepath.Join(media, "ABF-018.nfo")
	original, _ := os.ReadFile(file)
	version := app.db.Version("g:version")
	for _, payload := range []string{`null`, `[]`, `{}`, `{"plot":null}`, `{"genres":null}`, `{"lock_data":null}`, `{"year":null}`, `{"locked_fields":null}`, `{"plot":42}`, `{"year":"2020"}`, `{"year":-1}`, `{"year":1.5}`, `{"genres":"wrong"}`, `{"genres":[null]}`, `{"locked_fields":[null]}`, `{"lock_data":0}`, `{"unknown":"value"}`, `{"Genres":[null]}`, `{"Title":""}`, `{"title":"valid","TITLE":""}`, `{"plot":"x"} {}`, `{"plot":`} {
		t.Run(payload, func(t *testing.T) {
			resp, _ := embyCall(t, ts, "PATCH", path, token, payload)
			if resp.StatusCode != 400 {
				t.Fatal(resp.StatusCode)
			}
			after, _ := os.ReadFile(file)
			if string(after) != string(original) || app.db.Version("g:version") != version {
				t.Fatal("rejected patch mutated state")
			}
		})
	}
	resp, _ := embyCall(t, ts, "PATCH", path, "", `{"plot":""}`)
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	if !app.claimNFO("fixture") {
		t.Fatal("gate")
	}
	resp, _ = embyCall(t, ts, "PATCH", path, token, `{"plot":""}`)
	app.releaseNFO("fixture")
	if resp.StatusCode != 409 {
		t.Fatal(resp.StatusCode)
	}
}

func TestManualPatchClearPartialSuccessRecovery(t *testing.T) {
	root := t.TempDir()
	app, media := detailTestApp(t, root, patchFixture)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", filepath.Join(root, "t.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`CREATE TRIGGER fail_patch BEFORE UPDATE OF status ON movies BEGIN SELECT RAISE(ABORT,'private-patch-failure'); END`); err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/items/" + strconv.FormatInt(id, 10)
	resp, body := embyCall(t, ts, "PATCH", path, token, `{"plot":""}`)
	if resp.StatusCode != 200 || body["status"] != "partial_success" || body["saved"] != true || body["indexed"] != false {
		t.Fatalf("partial=%d %+v", resp.StatusCode, body)
	}
	meta, err := nfo.Read(filepath.Join(media, "ABF-018.nfo"))
	if err != nil || meta.Plot != "" {
		t.Fatalf("clear not saved: %+v %v", meta, err)
	}
	if _, err := fixture.Exec(`DROP TRIGGER fail_patch`); err != nil {
		t.Fatal(err)
	}
	recovery := body["recovery"].(map[string]any)
	payload := `{"nfo_sha256":"` + recovery["nfo_sha256"].(string) + `"}`
	resp, body = embyCall(t, ts, "POST", path+"/recover-edit", token, payload)
	if resp.StatusCode != 200 || body["status"] != "success" {
		t.Fatalf("recovery=%d %+v", resp.StatusCode, body)
	}
	movie, err := app.db.Movie(id)
	if err != nil || movie.Plot != "" || movie.Title != "Old" {
		t.Fatalf("recovered index=%+v %v", movie, err)
	}
}
