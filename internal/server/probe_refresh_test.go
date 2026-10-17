package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeReportsSavedFilesWithUnconfirmedVersions(t *testing.T) {
	for _, fault := range []string{"none", "global", "library", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			var targets []probeTarget
			for i, id := range []int64{f.victim, f.victim, f.survivor} {
				m, err := f.a.db.Movie(id)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				m.SourcePath = filepath.Join(dir, "movie.strm")
				m.NFOPath = filepath.Join(dir, "movie.nfo")
				writeFile(t, m.SourcePath, "https://fixture.invalid/movie.mp4\n")
				writeFile(t, m.NFOPath, "<movie><title>Keep</title></movie>")
				raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"duration":"12.5","size":"12345"}}`)
				if err = saveMediaInfo(m.SourcePath, "https://fixture.invalid/movie.mp4", raw); err != nil {
					t.Fatal(err)
				}
				targets = append(targets, probeTarget{Movie: m, Path: m.SourcePath, Primary: true, Index: i + 1})
			}
			version := func(key string) int {
				var raw string
				if err := f.db.QueryRow("SELECT value FROM kv WHERE key=?", key).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				v, err := strconv.Atoi(raw)
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			g := version("g:version")
			l := version(fmt.Sprintf("lib:%d:version", f.lib))
			other := version(fmt.Sprintf("lib:%d:version", f.other))
			var ddl string
			switch fault {
			case "global":
				ddl = "CREATE TRIGGER fail_probe_refresh BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'PROBE_REFRESH_SQL_CANARY'); END"
			case "library":
				ddl = fmt.Sprintf("CREATE TRIGGER fail_probe_refresh BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'PROBE_REFRESH_SQL_CANARY'); END", f.lib)
			case "commit":
				ddl = fmt.Sprintf("CREATE TABLE probe_refresh_parent(id INTEGER PRIMARY KEY); CREATE TABLE probe_refresh_child(id INTEGER REFERENCES probe_refresh_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_probe_refresh AFTER UPDATE ON kv WHEN NEW.key='lib:%d:version' BEGIN INSERT INTO probe_refresh_child VALUES(999); END", f.lib)
			}
			if ddl != "" {
				if _, err := f.db.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			f.a.cache.Set("refresh:stale", []byte("stale"), time.Hour)
			if !f.a.beginProbe(len(targets)) {
				t.Fatal("cannot start")
			}
			taskID := f.a.startTask("probe")
			// A missing executable guarantees this path must use saved raw output, not ffprobe/network.
			err := f.a.runProbe(taskID, "/fixture/does-not-exist-ffprobe", targets, true)
			wantFailures := 0
			wantGlobal := g + 2
			wantLibrary := l + 1
			wantOther := other + 1
			if fault != "none" {
				wantFailures = 1
				wantGlobal = g + 1
				wantLibrary = l
			}
			if fault == "global" {
				wantFailures = 2
				wantGlobal = g
				wantOther = other
			}
			if (err != nil) != (fault != "none") {
				t.Fatalf("result=%v fault=%s", err, fault)
			}
			if version("g:version") != wantGlobal || version(fmt.Sprintf("lib:%d:version", f.lib)) != wantLibrary || version(fmt.Sprintf("lib:%d:version", f.other)) != wantOther || f.a.db.Version("g:version") != strconv.Itoa(wantGlobal) {
				t.Fatal("deduplication, independent library refresh, or rollback failed")
			}
			if _, ok := f.a.cache.Get("refresh:stale"); ok {
				t.Fatal("did not clear local cache after saved files")
			}
			for _, target := range targets {
				raw, readErr := os.ReadFile(target.Movie.NFOPath)
				if readErr != nil || !strings.Contains(string(raw), "<probeversion>") {
					t.Fatalf("file not saved: %s %v", raw, readErr)
				}
			}
			req := httptest.NewRequest("GET", "/api/admin/probe/media/progress", nil)
			req.Header.Set("X-Emby-Token", "fixture-admin-delete")
			w := httptest.NewRecorder()
			f.a.Handler().ServeHTTP(w, req)
			var status probeStatus
			if json.Unmarshal(w.Body.Bytes(), &status) != nil || w.Code != 200 {
				t.Fatalf("status=%d %s", w.Code, w.Body.String())
			}
			if status.Running || status.Success != 3 || status.Done != 3 || status.Failed != 0 || status.RefreshFailed != wantFailures || status.FinishedAt == "" {
				t.Fatalf("wrong status: %+v", status)
			}
			if fault != "none" && (status.Error == "" || strings.Contains(status.Error, "CANARY") || strings.Contains(status.Error, "FOREIGN KEY")) {
				t.Fatalf("hidden/leaked failure: %+v", status)
			}
			f.a.taskMu.Lock()
			task := f.a.tasks[0]
			f.a.taskMu.Unlock()
			wantTask := "success"
			if fault != "none" {
				wantTask = "failed"
			}
			if task.Status != wantTask || task.EndedAt == "" {
				t.Fatalf("task=%+v", task)
			}
			if !f.a.claimNFO("fixture-check") {
				t.Fatal("NFO gate leaked")
			}
			f.a.releaseNFO("fixture-check")
			if fault != "none" {
				if _, err := f.db.Exec("DROP TRIGGER fail_probe_refresh"); err != nil {
					t.Fatal(err)
				}
			}
			// A new run resets the additive failure field; already current files skip locally.
			if !f.a.beginProbe(len(targets)) {
				t.Fatal("cannot restart")
			}
			if err := f.a.runProbe(f.a.startTask("probe"), "/fixture/does-not-exist-ffprobe", targets, true); err != nil {
				t.Fatal(err)
			}
			f.a.probeTaskMu.RLock()
			reset := f.a.probeStatus
			f.a.probeTaskMu.RUnlock()
			if reset.RefreshFailed != 0 || reset.Error != "" || reset.Skipped != 3 {
				t.Fatalf("old failure leaked: %+v", reset)
			}
		})
	}
}
