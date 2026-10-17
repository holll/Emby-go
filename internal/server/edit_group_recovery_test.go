package server

import (
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEditedNFORecoveryNeverDeletesInvalidCDGroupMember(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), `<movie><title>Old</title></movie>`)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	cd2 := filepath.Join(media, "GROUP-CD2.strm")
	cd3 := filepath.Join(media, "GROUP-CD3.strm")
	for _, file := range []string{cd2, cd3} {
		writeFile(t, file, "https://media.test/video.mp4\n")
		writeFile(t, file[:len(file)-5]+".nfo", `<movie><title>Group</title></movie>`)
	}
	// No CD1: both CD2/CD3 are existing independent records.
	scanOnce(t, ts, token)
	id2, err := app.db.MovieIDByPath(cd2)
	if err != nil {
		t.Fatal(err)
	}
	id3, err := app.db.MovieIDByPath(cd3)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cd3, "ed2k://invalid\n")
	path := "/api/admin/items/" + strconv.FormatInt(id2, 10)
	resp, body := embyCall(t, ts, "PUT", path, token, `{"title":"Saved CD2"}`)
	if resp.StatusCode != 200 || body["status"] != "partial_success" {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
	for _, id := range []int64{id2, id3} {
		if _, err := app.db.Movie(id); err != nil {
			t.Fatalf("edit deleted group member %d: %v", id, err)
		}
	}
	digest, err := nfoDigest(filepath.Join(media, "GROUP-CD2.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	resp, body = embyCall(t, ts, "POST", path+"/recover-edit", token, `{"nfo_sha256":"`+digest+`"}`)
	if resp.StatusCode != 200 || body["status"] != "partial_success" {
		t.Fatalf("%d %+v", resp.StatusCode, body)
	}
	for _, id := range []int64{id2, id3} {
		if _, err := app.db.Movie(id); err != nil {
			t.Fatalf("recovery deleted group member %d: %v", id, err)
		}
	}
}

func TestEditedNFORecoveryKeepsConfirmedDigestBaseline(t *testing.T) {
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
	m, err := app.db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	path := editNFOPath(m)
	confirmed, err := nfoDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `<movie><title>External</title></movie>`)
	if !app.claimNFO("edit") {
		t.Fatal("gate")
	}
	defer app.releaseNFO("edit")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	app.finishEditedNFO(c, m, path, confirmed)
	if w.Code != 409 {
		t.Fatalf("silently changed baseline: %d %s", w.Code, w.Body.String())
	}
	indexed, err := app.db.Movie(id)
	if err != nil || indexed.Status != m.Status || indexed.Title != m.Title {
		t.Fatalf("modified index: %+v %v", indexed, err)
	}
	if err := app.db.MarkManual("/nonexistent/fixture.strm", "/nonexistent/fixture.nfo"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("zero-row mark=%v", err)
	}
}
