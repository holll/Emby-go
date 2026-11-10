package server

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

func subtitleMarksTestMovie(t *testing.T, body string) (*App, *httptest.Server, string, store.Movie) {
	t.Helper()
	app, media := detailTestApp(t, t.TempDir(), body)
	if _, err := app.db.AddLibrary("Subtitle fixture", media); err != nil {
		t.Fatal(err)
	}
	ts, token := detailServer(t, app)
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	movie, err := app.db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	return app, ts, token, movie
}

func TestSubtitleMarksLocalPreviewCommitAndRemove(t *testing.T) {
	const original = `<movie><title>Original</title><actor><name>Actor</name></actor><custom>keep</custom><!--unchanged--></movie>`
	app, ts, token, movie := subtitleMarksTestMovie(t, original)
	media := filepath.Dir(movie.SourcePath)
	path := "/api/admin/items/" + strconv.FormatInt(movie.ID, 10) + "/subtitle-marks"
	writeFile(t, filepath.Join(media, "ABF-018.zh.srt"), "subtitle bytes")
	writeFile(t, filepath.Join(media, "poster.jpg"), "image bytes")
	seedFixturePlayback(t, app, movie.ID, 1234567, 3, "2026-11-06T00:00:00Z", 1234567)
	if _, err := app.db.SetIndexedFavoriteDataForUser(1, movie.ID, true); err != nil {
		t.Fatal(err)
	}
	userBefore, err := app.db.DataUser(1, movie.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := app.db.Version("g:version")
	resp, preview := embyCall(t, ts, "POST", path+"/preview", token, `{}`)
	if resp.StatusCode != 200 || preview["changed"] != true || preview["overwrite"] != false || preview["version"] == "" {
		t.Fatalf("preview %d %+v", resp.StatusCode, preview)
	}
	for _, field := range []string{"before", "after", "owned", "previous_owned"} {
		if _, ok := preview[field].([]any); !ok {
			t.Fatalf("%s not array: %+v", field, preview)
		}
	}
	if _, ok := preview["evidence"].(map[string]any)["matches"].([]any); !ok {
		t.Fatalf("matches not array: %+v", preview)
	}
	raw, err := os.ReadFile(movie.NFOPath)
	if err != nil || string(raw) != original || app.db.Version("g:version") != version {
		t.Fatalf("preview mutated source/version: %s %v", raw, err)
	}
	payload, _ := json.Marshal(map[string]any{"version": preview["version"], "overwrite": false})
	resp, result := embyCall(t, ts, "POST", path, token, string(payload))
	if resp.StatusCode != 200 || result["status"] != "success" || result["indexed"] != true || result["ownership_saved"] != true {
		t.Fatalf("confirm %d %+v", resp.StatusCode, result)
	}
	updated, err := app.db.Movie(movie.ID)
	if err != nil || len(updated.AutoMarks) != 1 || !slices.Equal(updated.Tags, updated.AutoMarks) || updated.Title != movie.Title || updated.Status != movie.Status || updated.LastScrapeAt != movie.LastScrapeAt || updated.LastScrapeError != movie.LastScrapeError {
		t.Fatalf("unexpected index/scrape result %+v %v", updated, err)
	}
	userAfter, err := app.db.DataUser(1, movie.ID)
	if err != nil || !reflect.DeepEqual(userBefore, userAfter) {
		t.Fatalf("personal data changed %+v %v", userAfter, err)
	}
	raw, _ = os.ReadFile(movie.NFOPath)
	if !strings.Contains(string(raw), `<actor><name>Actor</name></actor><custom>keep</custom><!--unchanged-->`) {
		t.Fatalf("unrelated NFO changed %s", raw)
	}
	image, _ := os.ReadFile(filepath.Join(media, "poster.jpg"))
	if string(image) != "image bytes" {
		t.Fatal("image changed")
	}
	// A fresh fill-only preview is protected, and an already-current index is a DB no-op.
	_, preview = embyCall(t, ts, "POST", path+"/preview", token, `{}`)
	version = app.db.Version("g:version")
	payload, _ = json.Marshal(map[string]any{"version": preview["version"]})
	resp, result = embyCall(t, ts, "POST", path, token, string(payload))
	if resp.StatusCode != 200 || result["status"] != "unchanged" || app.db.Version("g:version") != version {
		t.Fatalf("no-op %d %+v", resp.StatusCode, result)
	}
	// Removal requires explicit overwrite; ordinary fill does not revoke old tags.
	file := filepath.Join(media, "ABF-018.zh.srt")
	if filepath.Clean(file) != filepath.Join(media, "ABF-018.zh.srt") {
		t.Fatal("unexpected fixture removal path")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, preview = embyCall(t, ts, "POST", path+"/preview", token, `{"overwrite":true}`)
	payload, _ = json.Marshal(map[string]any{"version": preview["version"], "overwrite": true})
	resp, result = embyCall(t, ts, "POST", path, token, string(payload))
	updated, err = app.db.Movie(movie.ID)
	if resp.StatusCode != 200 || result["status"] != "success" || err != nil || len(updated.AutoMarks)+len(updated.Tags) != 0 {
		t.Fatalf("remove %d %+v %+v %v", resp.StatusCode, result, updated, err)
	}
}

func TestSubtitleMarksRejectChangedEvidence(t *testing.T) {
	for _, change := range []string{"nfo", "subtitle", "ownership", "source", "mode", "parts"} {
		t.Run(change, func(t *testing.T) {
			app, ts, token, movie := subtitleMarksTestMovie(t, `<movie><title>Old</title><tag>human</tag></movie>`)
			path := "/api/admin/items/" + strconv.FormatInt(movie.ID, 10) + "/subtitle-marks"
			_, preview := embyCall(t, ts, "POST", path+"/preview", token, `{"overwrite":true}`)
			overwrite := true
			switch change {
			case "nfo":
				writeFile(t, movie.NFOPath, `<movie><title>External edit</title><tag>human</tag></movie>`)
			case "subtitle":
				writeFile(t, filepath.Join(filepath.Dir(movie.SourcePath), "ABF-018.zh.srt"), "changed evidence")
			case "ownership":
				if err := app.db.SetAutoMarks(movie.ID, []string{"human"}); err != nil {
					t.Fatal(err)
				}
			case "source":
				writeFile(t, movie.SourcePath, "https://media.test/changed.mp4")
			case "mode":
				overwrite = false
			case "parts":
				fixture, err := sql.Open("sqlite", filepath.Join(filepath.Dir(filepath.Dir(movie.SourcePath)), "t.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer fixture.Close()
				if _, err := fixture.Exec(`UPDATE movies SET additional_parts='["changed-CD2.strm"]' WHERE id=?`, movie.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(movie.NFOPath)
			version := app.db.Version("g:version")
			payload, _ := json.Marshal(map[string]any{"version": preview["version"], "overwrite": overwrite})
			resp, result := embyCall(t, ts, "POST", path, token, string(payload))
			after, _ := os.ReadFile(movie.NFOPath)
			if resp.StatusCode != 409 || string(before) != string(after) || app.db.Version("g:version") != version {
				t.Fatalf("stale confirmation mutated: %d %+v", resp.StatusCode, result)
			}
		})
	}
}

func TestSubtitleMarksPartialCommit(t *testing.T) {
	for _, fault := range []string{"ownership", "index", "both", "nfo"} {
		t.Run(fault, func(t *testing.T) {
			app, ts, token, movie := subtitleMarksTestMovie(t, `<movie><title>Old</title><actor><name>Actor</name></actor></movie>`)
			path := "/api/admin/items/" + strconv.FormatInt(movie.ID, 10) + "/subtitle-marks"
			writeFile(t, filepath.Join(filepath.Dir(movie.SourcePath), "ABF-018.zh.srt"), "subtitle")
			fixture, err := sql.Open("sqlite", filepath.Join(filepath.Dir(filepath.Dir(movie.SourcePath)), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()
			if fault == "ownership" || fault == "both" {
				if _, err := fixture.Exec(`CREATE TRIGGER fail_marks BEFORE UPDATE OF auto_marks ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_OWNERSHIP_CANARY'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "index" || fault == "both" {
				if _, err := fixture.Exec(`CREATE TRIGGER fail_marks_index BEFORE UPDATE OF title ON movies BEGIN SELECT RAISE(ABORT,'PRIVATE_INDEX_CANARY'); END`); err != nil {
					t.Fatal(err)
				}
			}
			_, preview := embyCall(t, ts, "POST", path+"/preview", token, `{}`)
			if fault == "nfo" {
				// Stable existing directory at the actual temp destination causes the
				// real atomic writer to fail before rename, even when tests run as root.
				if err := os.Mkdir(movie.NFOPath+".tmp", 0755); err != nil {
					t.Fatal(err)
				}
			}
			payload, _ := json.Marshal(map[string]any{"version": preview["version"]})
			resp, result := embyCall(t, ts, "POST", path, token, string(payload))
			updated, err := app.db.Movie(movie.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "nfo" {
				if resp.StatusCode != 422 || len(updated.AutoMarks)+len(updated.Tags) != 0 {
					t.Fatalf("NFO failure committed: %d %+v %+v", resp.StatusCode, result, updated)
				}
				return
			}
			meta, err := nfo.Read(movie.NFOPath)
			if resp.StatusCode != 200 || result["status"] != "partial_success" || result["saved"] != true || err != nil || len(meta.Tags) != 1 {
				t.Fatalf("partial %d %+v %+v %v", resp.StatusCode, result, meta, err)
			}
			if (result["ownership_saved"] == false) != (fault == "ownership" || fault == "both") || (result["indexed"] == false) != (fault == "index" || fault == "both") {
				t.Fatalf("wrong stages %+v", result)
			}
			serialized, _ := json.Marshal(result)
			if strings.Contains(string(serialized), "PRIVATE_") || strings.Contains(string(serialized), movie.NFOPath) || updated.LastScrapeAt != movie.LastScrapeAt || updated.Status != movie.Status {
				t.Fatalf("leak/scrape side effect %s %+v", serialized, updated)
			}
		})
	}
}

func TestSubtitleMarksStackedSharedNFO(t *testing.T) {
	const unrelated = `<movie><title>Unrelated</title><tag>human</tag></movie>`
	app, ts, token, other := subtitleMarksTestMovie(t, unrelated)
	media := filepath.Dir(other.SourcePath)
	main := filepath.Join(media, "STACK-CD1.strm")
	part := filepath.Join(media, "STACK-CD2.strm")
	shared := filepath.Join(media, "STACK.nfo")
	writeFile(t, main, "https://media.test/stack-1.mp4\n")
	writeFile(t, part, "https://media.test/stack-2.mp4\n")
	writeFile(t, shared, `<movie><title>Stack</title></movie>`)
	writeFile(t, filepath.Join(media, "STACK-CD2.zh.srt"), "CD2 only")
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(main)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := app.db.Movie(id)
	if err != nil || movie.NFOPath != shared || !slices.Equal(movie.AdditionalParts, []string{part}) {
		t.Fatalf("stack fixture %+v %v", movie, err)
	}
	other, err = app.db.Movie(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/items/" + strconv.FormatInt(id, 10) + "/subtitle-marks"
	resp, preview := embyCall(t, ts, "POST", path+"/preview", token, `{}`)
	if resp.StatusCode != 200 || preview["changed"] != false || preview["main_file"] != "STACK-CD1.strm" || preview["evidence"].(map[string]any)["source"] != "none" {
		t.Fatalf("CD2 was incorrectly aggregated: %d %+v", resp.StatusCode, preview)
	}
	if _, ok := preview["evidence"].(map[string]any)["matches"].([]any); !ok {
		t.Fatalf("empty matches not array: %+v", preview)
	}
	payload, _ := json.Marshal(map[string]any{"version": preview["version"]})
	resp, result := embyCall(t, ts, "POST", path, token, string(payload))
	if resp.StatusCode != 200 || result["status"] != "unchanged" {
		t.Fatalf("CD2-only confirmation %d %+v", resp.StatusCode, result)
	}
	writeFile(t, filepath.Join(media, "STACK-CD1.zh.srt"), "main subtitle")
	_, preview = embyCall(t, ts, "POST", path+"/preview", token, `{}`)
	payload, _ = json.Marshal(map[string]any{"version": preview["version"]})
	resp, result = embyCall(t, ts, "POST", path, token, string(payload))
	updated, err := app.db.Movie(id)
	if resp.StatusCode != 200 || result["status"] != "success" || err != nil || updated.NFOPath != shared || !slices.Equal(updated.AdditionalParts, []string{part}) || !slices.Equal(updated.AutoMarks, []string{"中文字幕"}) || !slices.Equal(updated.Tags, updated.AutoMarks) {
		t.Fatalf("shared NFO confirmation %d %+v %+v %v", resp.StatusCode, result, updated, err)
	}
	meta, err := nfo.Read(shared)
	if err != nil || !slices.Equal(meta.Tags, updated.Tags) {
		t.Fatalf("shared NFO not updated %+v %v", meta, err)
	}
	unchanged, err := app.db.Movie(other.ID)
	raw, readErr := os.ReadFile(other.NFOPath)
	if err != nil || readErr != nil || string(raw) != unrelated || !reflect.DeepEqual(unchanged, other) {
		t.Fatalf("unrelated movie changed %+v %v %v", unchanged, err, readErr)
	}
	for _, source := range []string{main, part} {
		raw, err := os.ReadFile(source)
		if err != nil || !strings.HasPrefix(string(raw), "https://media.test/stack-") {
			t.Fatalf("STRM changed %s %v", raw, err)
		}
	}
}

func TestSubtitleMarksProtectedWhitespaceAndStaleIndex(t *testing.T) {
	for _, tag := range []string{" human ", " \t\n "} {
		for _, stale := range []bool{false, true} {
			t.Run(strconv.Quote(tag)+"/stale="+strconv.FormatBool(stale), func(t *testing.T) {
				original := `<movie><title>Original</title><tag>` + tag + `</tag><custom>keep</custom></movie>`
				app, ts, token, movie := subtitleMarksTestMovie(t, original)
				if err := app.db.MarkIndexedManual(movie.SourcePath, movie.NFOPath); err != nil {
					t.Fatal(err)
				}
				if stale {
					fixture, err := sql.Open("sqlite", filepath.Join(filepath.Dir(filepath.Dir(movie.SourcePath)), "t.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer fixture.Close()
					if _, err := fixture.Exec(`UPDATE movies SET tags='[]' WHERE id=?`, movie.ID); err != nil {
						t.Fatal(err)
					}
				}
				path := "/api/admin/items/" + strconv.FormatInt(movie.ID, 10) + "/subtitle-marks"
				for attempt := range 2 {
					resp, preview := embyCall(t, ts, "POST", path+"/preview", token, `{}`)
					if resp.StatusCode != 200 || preview["protected"] != true || preview["changed"] != false {
						t.Fatalf("protected preview %d %+v", resp.StatusCode, preview)
					}
					version := app.db.Version("g:version")
					payload, _ := json.Marshal(map[string]any{"version": preview["version"]})
					resp, result := embyCall(t, ts, "POST", path, token, string(payload))
					want := "unchanged"
					if stale && attempt == 0 {
						want = "success"
					}
					if resp.StatusCode != 200 || result["status"] != want || result["indexed"] != true {
						t.Fatalf("confirm %d %+v", resp.StatusCode, result)
					}
					if (app.db.Version("g:version") == version) != (want == "unchanged") {
						t.Fatal("wrong index version change", want)
					}
					updated, err := app.db.Movie(movie.ID)
					if err != nil || !slices.Equal(updated.Tags, []string{tag}) || updated.Status != "manual" || updated.Title != movie.Title || updated.LastScrapeAt != movie.LastScrapeAt {
						t.Fatalf("raw/manual index changed: %+v %v", updated, err)
					}
					raw, err := os.ReadFile(movie.NFOPath)
					if err != nil || string(raw) != original {
						t.Fatalf("protected bytes changed: %q %v", raw, err)
					}
				}
			})
		}
	}
}

func TestSubtitleMarksInputAndGates(t *testing.T) {
	app, ts, token, movie := subtitleMarksTestMovie(t, `<movie><title>Old</title></movie>`)
	path := "/api/admin/items/" + strconv.FormatInt(movie.ID, 10) + "/subtitle-marks"
	before, _ := os.ReadFile(movie.NFOPath)
	version := app.db.Version("g:version")
	for _, payload := range []string{`null`, `[]`, `{"overwrite":null}`, `{"overwrite":"true"}`, `{"Overwrite":true}`, `{"path":"/tmp/other"}`, `{"tags":[]}`, `{} {}`, `{"version":"bad"}`, `{"overwrite":true,`} {
		resp, _ := embyCall(t, ts, "POST", path+"/preview", token, payload)
		if resp.StatusCode != 400 {
			t.Fatalf("preview input %q got %d", payload, resp.StatusCode)
		}
	}
	resp, _ := embyCall(t, ts, "POST", path, token, `{}`)
	if resp.StatusCode != 400 {
		t.Fatal("confirmation accepted no version")
	}
	resp, _ = embyCall(t, ts, "POST", path+"/preview", "", `{}`)
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous %d", resp.StatusCode)
	}
	_, preview := embyCall(t, ts, "POST", path+"/preview", token, `{}`)
	payload, _ := json.Marshal(map[string]any{"version": preview["version"]})
	for _, owner := range []string{"scan", "probe", "edit", "scrape", "subtitle_marks"} {
		if !app.claimNFO(owner) {
			t.Fatal("fixture claim", owner)
		}
		resp, _ := embyCall(t, ts, "POST", path, token, string(payload))
		app.releaseNFO(owner)
		if resp.StatusCode != 409 {
			t.Fatalf("owner %s allowed concurrent confirmation: %d", owner, resp.StatusCode)
		}
	}
	app.stopWorkerAdmission()
	for _, suffix := range []string{"", "/preview"} {
		body := string(payload)
		if suffix != "" {
			body = `{}`
		}
		resp, _ := embyCall(t, ts, "POST", path+suffix, token, body)
		if resp.StatusCode != 503 {
			t.Fatalf("shutdown %s: %d", suffix, resp.StatusCode)
		}
	}
	after, _ := os.ReadFile(movie.NFOPath)
	if string(before) != string(after) || app.db.Version("g:version") != version {
		t.Fatal("rejected operations mutated NFO/version")
	}
}
