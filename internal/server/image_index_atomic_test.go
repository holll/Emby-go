package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"emby-go/internal/store"
)

type imageUploadReader struct {
	io.Reader
	once       sync.Once
	beforeRead func()
}

func (r *imageUploadReader) Read(p []byte) (int, error) {
	r.once.Do(r.beforeRead)
	return r.Reader.Read(p)
}

func uploadImageFixture(t *testing.T, f adminDeleteFixture, kind, token string, beforeRead func()) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "fixture.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(part, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	if beforeRead == nil {
		beforeRead = func() {}
	}
	reader := &imageUploadReader{Reader: bytes.NewReader(body.Bytes()), beforeRead: beforeRead}
	req := httptest.NewRequest("POST", "/api/admin/items/"+strconv.FormatInt(f.victim, 10)+"/images/"+kind, reader)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, req)
	return w
}

func imageOutputDir(t *testing.T, f adminDeleteFixture) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := f.db.Exec("UPDATE movies SET output_dir=? WHERE id=?", dir, f.victim); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImageUploadDoesNotRestoreStaleMetadataOrDeletedMovie(t *testing.T) {
	for _, mode := range []string{"metadata", "move", "delete"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			dir := imageOutputDir(t, f)
			original, err := f.a.db.Movie(f.victim)
			if err != nil {
				t.Fatal(err)
			}
			var afterConcurrent string
			var globalBeforeUpload int
			w := uploadImageFixture(t, f, "primary", "fixture-admin-delete", func() {
				if mode == "delete" {
					if err := f.a.db.DeleteIndexedMovie(f.victim); err != nil {
						t.Fatal(err)
					}
				} else {
					original.Title = "new concurrent title"
					original.Plot = "new concurrent plot"
					original.Status = "manual"
					if mode == "move" {
						original.LibraryID = f.other
					}
					if _, err := f.a.db.IndexMovie(original, 77, time.Unix(88, 0), []store.ActorRef{{Name: "New concurrent actor"}}); err != nil {
						t.Fatal(err)
					}
				}
				afterConcurrent = adminDeleteSnapshot(t, f)
				var version string
				if err := f.db.QueryRow("SELECT value FROM kv WHERE key='g:version'").Scan(&version); err != nil {
					t.Fatal(err)
				}
				globalBeforeUpload, err = strconv.Atoi(version)
				if err != nil {
					t.Fatal(err)
				}
			})
			raw, err := os.ReadFile(filepath.Join(dir, "poster.webp"))
			if err != nil || len(raw) < 12 || string(raw[:4]) != "RIFF" {
				t.Fatalf("saved WebP: %v len=%d", err, len(raw))
			}
			if mode == "delete" {
				if w.Code != 500 || adminDeleteSnapshot(t, f) != afterConcurrent {
					t.Fatalf("deleted movie resurrected/status=%d body=%s", w.Code, w.Body.String())
				}
				var response map[string]any
				if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response["saved"] != true || response["indexed"] != false || response["code"] != "IMAGE_INDEX_FAILED" {
					t.Fatalf("file-only result=%v", response)
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("upload status=%d body=%s", w.Code, w.Body.String())
			}
			got, err := f.a.db.Movie(f.victim)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != original.Title || got.Plot != original.Plot || got.Status != "manual" || got.LibraryID != original.LibraryID || got.PosterPath != filepath.Join(dir, "poster.webp") {
				t.Fatalf("stale metadata restored: %+v", got)
			}
			if gotVersion := f.a.db.Version("g:version"); gotVersion != strconv.Itoa(globalBeforeUpload+1) {
				t.Fatalf("global=%s want=%d", gotVersion, globalBeforeUpload+1)
			}
			var size int
			var actor string
			if err = f.db.QueryRow("SELECT file_size FROM movies WHERE id=?", f.victim).Scan(&size); err != nil {
				t.Fatal(err)
			}
			if err = f.db.QueryRow("SELECT actor_name FROM movie_actors WHERE movie_id=?", f.victim).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			if size != 77 || actor != "New concurrent actor" {
				t.Fatalf("upload touched source stat/actors: %d %s", size, actor)
			}
		})
	}
}

func TestImageUploadIndexFailureIsExplicitFileOnlyResult(t *testing.T) {
	for _, fault := range []string{"global", "library", "update", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			dir := imageOutputDir(t, f)
			// Authentication still happens before consuming/uploading a request body.
			before := adminDeleteSnapshot(t, f)
			if w := uploadImageFixture(t, f, "poster", "", func() { t.Fatal("unauthenticated request consumed image") }); w.Code != 401 {
				t.Fatalf("unauthorized=%d", w.Code)
			}
			if adminDeleteSnapshot(t, f) != before {
				t.Fatal("unauthenticated update changed DB")
			}
			var ddl string
			switch fault {
			case "global":
				ddl = "CREATE TRIGGER fail_image_api BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'IMAGE_SQL_CANARY'); END"
			case "library":
				ddl = fmt.Sprintf("CREATE TRIGGER fail_image_api BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'IMAGE_SQL_CANARY'); END", f.lib)
			case "update":
				ddl = "CREATE TRIGGER fail_image_api BEFORE UPDATE ON movies BEGIN SELECT RAISE(ABORT,'IMAGE_SQL_CANARY'); END"
			case "commit":
				ddl = "CREATE TABLE api_image_parent(id INTEGER PRIMARY KEY); CREATE TABLE api_image_child(id INTEGER REFERENCES api_image_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_image_api AFTER UPDATE ON movies BEGIN INSERT INTO api_image_child VALUES(999); END"
			}
			if _, err := f.db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			w := uploadImageFixture(t, f, "poster", "fixture-admin-delete", nil)
			if w.Code != 500 || adminDeleteSnapshot(t, f) != before {
				t.Fatalf("failure status=%d changed database or versions: %s", w.Code, w.Body.String())
			}
			if bytes.Contains(w.Body.Bytes(), []byte("IMAGE_SQL_CANARY")) || bytes.Contains(w.Body.Bytes(), []byte("FOREIGN KEY")) {
				t.Fatalf("leaked SQL: %s", w.Body.String())
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response["saved"] != true || response["indexed"] != false || response["code"] != "IMAGE_INDEX_FAILED" {
				t.Fatalf("result=%v", response)
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "poster.webp")); err != nil || len(raw) == 0 {
				t.Fatalf("saved file missing: %v", err)
			}
			if _, err := f.db.Exec("DROP TRIGGER fail_image_api"); err != nil {
				t.Fatal(err)
			}
			if retry := uploadImageFixture(t, f, "poster", "fixture-admin-delete", nil); retry.Code != 200 {
				t.Fatalf("retry=%d %s", retry.Code, retry.Body.String())
			}
		})
	}
}
