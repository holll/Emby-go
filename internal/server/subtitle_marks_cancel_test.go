package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"emby-go/internal/nfo"
	"modernc.org/sqlite"
)

var subtitleMarksCancelSequence atomic.Uint64

// Cancellation is injected by SQLite at the actual ownership UPDATE, not by a
// production hook or a racing timer. AFTER observes a successful row update;
// BEFORE plus RAISE(ABORT) exercises ownership failure with cancellation together.
func TestSubtitleMarksCancellation(t *testing.T) {
	for _, stage := range []string{"before_write", "ownership_saved", "ownership_failed"} {
		t.Run(stage, func(t *testing.T) {
			var target atomic.Pointer[App]
			var ownershipCalls, indexCalls atomic.Int32
			name := fmt.Sprintf("subtitle_marks_cancel_%d", subtitleMarksCancelSequence.Add(1))
			// modernc installs registered functions when opening connections, so
			// registration must precede subtitleMarksTestMovie opening the App.
			if err := sqlite.RegisterScalarFunction(name, 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if args[0] == "index" {
					indexCalls.Add(1)
					return int64(0), nil
				}
				ownershipCalls.Add(1)
				// Read the real destination inside the UPDATE boundary: NFO must
				// already have been atomically committed before ownership is tried.
				meta, err := nfo.Read(args[1].(string))
				if err != nil {
					return nil, err
				}
				if !slices.Equal(meta.Tags, []string{"中文字幕"}) {
					return nil, fmt.Errorf("ownership reached before NFO commit: %v", meta.Tags)
				}
				target.Load().rootCancel()
				return int64(0), nil
			}); err != nil {
				t.Fatal(err)
			}

			const original = `<movie><title>Old</title><actor><name>Actor</name></actor><custom>keep</custom></movie>`
			app, ts, token, movie := subtitleMarksTestMovie(t, original)
			target.Store(app)
			writeFile(t, filepath.Join(filepath.Dir(movie.SourcePath), "ABF-018.zh.srt"), "subtitle")
			path := "/api/admin/items/" + strconv.FormatInt(movie.ID, 10) + "/subtitle-marks"
			resp, preview := embyCall(t, ts, http.MethodPost, path+"/preview", token, `{}`)
			if resp.StatusCode != http.StatusOK || preview["changed"] != true {
				t.Fatalf("preview: %d %+v", resp.StatusCode, preview)
			}
			payload, err := json.Marshal(map[string]any{"version": preview["version"]})
			if err != nil {
				t.Fatal(err)
			}
			fixture, err := sql.Open("sqlite", filepath.Join(filepath.Dir(filepath.Dir(movie.SourcePath)), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()
			// IndexMovie updates title even when its value is unchanged. This
			// witness records an attempted index write, even if later rolled back.
			if _, err := fixture.Exec(`CREATE TRIGGER witness_subtitle_index BEFORE UPDATE OF title ON movies BEGIN SELECT ` + name + `('index', NEW.nfo_path); END`); err != nil {
				t.Fatal(err)
			}
			when, fail := "AFTER", ""
			if stage == "ownership_failed" {
				when, fail = "BEFORE", ` SELECT RAISE(ABORT, 'PRIVATE_CANCEL_OWNERSHIP');`
			}
			if _, err := fixture.Exec(`CREATE TRIGGER cancel_subtitle_ownership ` + when + ` UPDATE OF auto_marks ON movies BEGIN SELECT ` + name + `('ownership', NEW.nfo_path);` + fail + ` END`); err != nil {
				t.Fatal(err)
			}
			version := app.db.Version("g:version")
			if stage == "before_write" {
				// Leave worker admission open: this must exercise the real canceled
				// root/request context, not the separate shutdown admission gate.
				app.rootCancel()
			}
			resp, result := embyCall(t, ts, http.MethodPost, path, token, string(payload))
			if !app.workerAdmissionOpen() || app.rootCtx.Err() != context.Canceled {
				t.Fatalf("wrong cancellation boundary: admission=%v root=%v", app.workerAdmissionOpen(), app.rootCtx.Err())
			}
			if app.nfoBusyOwner() != "" {
				t.Fatal("confirmation leaked NFO gate")
			}
			if indexCalls.Load() != 0 {
				t.Fatalf("index attempted after cancellation: %d", indexCalls.Load())
			}
			updated, err := app.db.Movie(movie.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(movie.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "before_write" {
				if resp.StatusCode != http.StatusServiceUnavailable || result["code"] != "AUTH_UNAVAILABLE" || result["saved"] != nil {
					t.Fatalf("canceled authorization: %d %+v", resp.StatusCode, result)
				}
				if string(raw) != original || !reflect.DeepEqual(updated, movie) || app.db.Version("g:version") != version || ownershipCalls.Load() != 0 {
					t.Fatalf("pre-write cancellation mutated file/DB: NFO=%s movie=%+v ownership=%d", raw, updated, ownershipCalls.Load())
				}
				return
			}
			if resp.StatusCode != http.StatusOK || result["status"] != "partial_success" || result["saved"] != true || result["canceled"] != true || result["indexed"] != false || result["refresh_error"] != "文件已确认保存，取消后未刷新索引；请重新扫描该影片。" {
				t.Fatalf("post-commit cancellation: %d %+v", resp.StatusCode, result)
			}
			if ownershipCalls.Load() != 1 {
				t.Fatalf("ownership not attempted exactly once: %d", ownershipCalls.Load())
			}
			meta, err := nfo.Read(movie.NFOPath)
			if err != nil || !slices.Equal(meta.Tags, []string{"中文字幕"}) || !strings.Contains(string(raw), `<actor><name>Actor</name></actor><custom>keep</custom>`) {
				t.Fatalf("committed NFO lost or unrelated content changed: %s %v", raw, err)
			}
			if stage == "ownership_saved" {
				if result["ownership_saved"] != true || result["auto_marks_error"] != nil || !slices.Equal(updated.AutoMarks, []string{"中文字幕"}) || app.db.Version("g:version") == version {
					t.Fatalf("ownership not committed after NFO: %+v movie=%+v", result, updated)
				}
				// Ownership is the only persisted movie change; tags, scrape state,
				// and all other indexed metadata must still be the old snapshot.
				updated.AutoMarks = movie.AutoMarks
			} else {
				if result["ownership_saved"] != false || result["auto_marks_error"] != "NFO 已确认保存，但归属记录失败；请人工核对，不要直接重新整理；重扫不能恢复归属。" || app.db.Version("g:version") != version {
					t.Fatalf("ownership failure/cancel information lost: %+v", result)
				}
			}
			if !reflect.DeepEqual(updated, movie) {
				t.Fatalf("canceled refresh changed indexed movie: before=%+v after=%+v", movie, updated)
			}
			serialized, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(serialized), "PRIVATE_CANCEL_OWNERSHIP") || strings.Contains(string(serialized), movie.NFOPath) {
				t.Fatalf("private failure leaked: %s", serialized)
			}
		})
	}
}
