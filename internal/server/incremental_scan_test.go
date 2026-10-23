package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"emby-go/internal/scanner"
	"emby-go/internal/scheduler"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type scanTaskView struct {
	TaskID int64  `json:"task_id"`
	Status string `json:"status"`
	Mode   string `json:"mode"`
	Error  string `json:"error"`
	Result *struct {
		Mode             string `json:"mode"`
		GroupsTotal      int    `json:"groups_total"`
		GroupsDone       int    `json:"groups_done"`
		UnchangedGroups  int    `json:"unchanged_groups"`
		Added            int    `json:"added"`
		Updated          int    `json:"updated"`
		Deleted          int    `json:"deleted"`
		IndexChanged     bool   `json:"index_changed"`
		CleanupPerformed bool   `json:"cleanup_performed"`
		Incomplete       bool   `json:"incomplete"`
	} `json:"result"`
}

func submitScan(t *testing.T, ts *httptest.Server, token, query, idem string) (*http.Response, scanTaskView) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/tasks/scan"+query, nil)
	req.Header.Set("X-Emby-Token", token)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var view scanTaskView
	_ = json.Unmarshal(raw, &view)
	return resp, view
}

func waitScanTask(t *testing.T, ts *httptest.Server, token string, id int64) scanTaskView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/tasks/"+strconv.FormatInt(id, 10), nil)
		req.Header.Set("X-Emby-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var view scanTaskView
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		switch view.Status {
		case "succeeded", "failed", "skipped":
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan task did not finish: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIncrementalScanSkipsUnchangedGroupsAndRebuildsOnChange(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	libraries, err := app.db.Libraries()
	if err != nil || len(libraries) != 1 {
		t.Fatalf("libraries=%v err=%v", libraries, err)
	}
	root := libraries[0].Path
	nfo := filepath.Join(root, "Test Movie (2020).nfo")
	if err := os.WriteFile(nfo, []byte("<movie><title>Original</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, first := submitScan(t, ts, token, "?mode=incremental", "inc-1")
	if resp.StatusCode != http.StatusAccepted || first.TaskID == 0 || first.Mode != "incremental" {
		t.Fatalf("first submit: %d %+v", resp.StatusCode, first)
	}
	done := waitScanTask(t, ts, token, first.TaskID)
	if done.Status != "succeeded" || done.Result == nil || done.Result.Mode != "incremental" || done.Result.GroupsTotal != 1 || done.Result.Added != 1 {
		t.Fatalf("first incremental result: %+v", done)
	}

	resp, second := submitScan(t, ts, token, "?mode=incremental", "inc-2")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("second submit: %d", resp.StatusCode)
	}
	done = waitScanTask(t, ts, token, second.TaskID)
	if done.Result == nil || done.Result.UnchangedGroups != 1 || done.Result.Added != 0 || done.Result.Updated != 0 || done.Result.IndexChanged {
		t.Fatalf("unchanged incremental result: %+v", done)
	}

	// Same size/mtime independent change: content is read and hashed, so the
	// changed title must be rebuilt even though stat() metadata is unchanged.
	if err := os.WriteFile(nfo, []byte("<movie><title>Changed!!</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, third := submitScan(t, ts, token, "?mode=incremental", "inc-3")
	done = waitScanTask(t, ts, token, third.TaskID)
	if done.Status != "succeeded" || done.Result == nil || done.Result.Updated != 1 || done.Result.UnchangedGroups != 0 {
		t.Fatalf("changed incremental result: %+v", done)
	}

	_, full := submitScan(t, ts, token, "?mode=full", "full-1")
	done = waitScanTask(t, ts, token, full.TaskID)
	if done.Status != "succeeded" || done.Result == nil || done.Result.Mode != "full" || done.Result.UnchangedGroups != 0 || done.Result.Added+done.Result.Updated == 0 {
		t.Fatalf("full result: %+v", done)
	}
}

func TestScanModeValidationAndIdempotencyConflict(t *testing.T) {
	_, ts, token := asyncScanFixture(t)
	if resp, _ := submitScan(t, ts, token, "?mode=sideways", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid mode should be 400, got %d", resp.StatusCode)
	}
	resp, created := submitScan(t, ts, token, "?mode=incremental", "same-key")
	if resp.StatusCode != http.StatusAccepted || created.TaskID == 0 {
		t.Fatalf("submit: %d %+v", resp.StatusCode, created)
	}
	waitScanTask(t, ts, token, created.TaskID)

	resp, replay := submitScan(t, ts, token, "?mode=incremental", "same-key")
	if resp.StatusCode != http.StatusAccepted || replay.TaskID != created.TaskID {
		t.Fatalf("same key/mode should replay %d: %d %+v", created.TaskID, resp.StatusCode, replay)
	}
	resp, _ = submitScan(t, ts, token, "?mode=full", "same-key")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("same key different mode should be 409, got %d", resp.StatusCode)
	}
	if resp, _ := submitScan(t, ts, token, "?mode=incremental", "other-key"); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("different key should create a task: %d", resp.StatusCode)
	}
}

func TestEmptyInventoryBlockedUnlessExplicitlyConfirmed(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	_, first := submitScan(t, ts, token, "?mode=full", "empty-1")
	if done := waitScanTask(t, ts, token, first.TaskID); done.Status != "succeeded" {
		t.Fatalf("baseline scan failed: %+v", done)
	}
	libraries, _ := app.db.Libraries()
	root := libraries[0].Path
	if err := os.Remove(filepath.Join(root, "Test Movie (2020).strm")); err != nil {
		t.Fatal(err)
	}

	_, blocked := submitScan(t, ts, token, "?mode=full", "empty-2")
	done := waitScanTask(t, ts, token, blocked.TaskID)
	if done.Status != "failed" || done.Result == nil || !done.Result.Incomplete || done.Result.CleanupPerformed || done.Result.Deleted != 0 {
		t.Fatalf("empty inventory must be blocked: %+v", done)
	}
	source := filepath.Join(root, "Test Movie (2020).strm")
	if _, err := app.db.MovieIDByPath(source); err != nil {
		t.Fatalf("blocked scan must preserve the index: %v", err)
	}

	resp, confirmed := submitScan(t, ts, token, "?mode=full&allow_empty_removal=1", "empty-3")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("confirm submit: %d", resp.StatusCode)
	}
	done = waitScanTask(t, ts, token, confirmed.TaskID)
	if done.Status != "succeeded" || done.Result == nil || !done.Result.CleanupPerformed || done.Result.Deleted != 1 {
		t.Fatalf("confirmed cleanup result: %+v", done)
	}
	if _, err := app.db.MovieIDByPath(source); err == nil {
		t.Fatal("confirmed cleanup should delete rows")
	}
}

func TestScanStrictParametersBeforeReplay(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	_, first := submitScan(t, ts, token, "", "strict-key")
	waitScanTask(t, ts, token, first.TaskID)
	for _, query := range []string{"?library_id=", "?library_id=%20", "?allow_empty_removal=", "?allow_empty_removal=%20", "?library_id=0&library_id=1", "?mode=full&mode=incremental", "?allow_empty_removal=false&allow_empty_removal=true", "?root_marker_path=x&root_marker_path=y", "?root_marker_sha256=x&root_marker_sha256=y", "?library_id=garbage", "?library_id=-1", "?library_id=1.5", "?mode=sideways", "?allow_empty_removal=yes", "?allow_empty_removal=garbage", "?allow_empty_removal=2", "?root_marker_path=marker", "?root_marker_sha256=" + strings.Repeat("a", 64), "?root_marker_path=../marker&root_marker_sha256=" + strings.Repeat("a", 64), "?root_marker_path=marker&root_marker_sha256=bad", "?root_marker_path=&root_marker_sha256="} {
		if resp, _ := submitScan(t, ts, token, query, "strict-key"); resp.StatusCode != 400 {
			t.Fatalf("invalid replay %s => %d", query, resp.StatusCode)
		}
		if w := translationCall(app, token, "POST", "/api/admin/scan"+query, ""); w.Code != 400 {
			t.Fatalf("invalid sync %s => %d %s", query, w.Code, w.Body.String())
		}
	}
	if resp, task := submitScan(t, ts, token, "?mode=FULL&library_id=0&allow_empty_removal=false", "strict-key"); resp.StatusCode != 202 || task.TaskID != first.TaskID {
		t.Fatalf("canonical replay %d %+v", resp.StatusCode, task)
	}
	if resp, _ := submitScan(t, ts, token, "?allow_empty_removal=true", "strict-key"); resp.StatusCode != 409 {
		t.Fatalf("different empty permission %d", resp.StatusCode)
	}
}

func TestScanRegistryConcurrentDigestValidation(t *testing.T) {
	app, _, _ := asyncScanFixture(t)
	const n = 24
	start := make(chan struct{})
	type outcome struct {
		task    asyncScanTask
		created bool
		err     error
		digest  string
	}
	out := make(chan outcome, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			digest := "A"
			if i%2 == 1 {
				digest = "B"
			}
			task, created, err := app.registerScanTask(int64(i+500), "scan", 0, scanner.ScanModeFull, false, digest, "concurrent", time.Now())
			out <- outcome{task, created, err, digest}
		}(i)
	}
	close(start)
	wg.Wait()
	close(out)
	var winner int64
	createdCount, conflicts, replayed := 0, 0, 0
	for result := range out {
		if errors.Is(result.err, errScanIdemConflict) {
			conflicts++
			continue
		}
		if result.err != nil {
			t.Fatal(result.err)
		}
		if winner == 0 {
			winner = result.task.ID
		}
		if result.task.ID != winner || result.task.IdemDigest != result.digest {
			t.Fatalf("cross-digest replay %+v", result)
		}
		if result.created {
			createdCount++
		} else {
			replayed++
		}
	}
	if createdCount != 1 || conflicts != n/2 || replayed != n/2-1 {
		t.Fatalf("created=%d conflict=%d replay=%d", createdCount, conflicts, replayed)
	}
	if !app.beginScan(1, winner, scanner.ScanModeFull) {
		t.Fatal("claim scan")
	}
	defer app.endScan(nil)
	original, _ := app.scanTaskByID(winner)
	if replay, created, err := app.registerScanTask(900, "scan", 0, scanner.ScanModeFull, false, original.IdemDigest, "concurrent", time.Now()); err != nil || created || replay.ID != winner {
		t.Fatalf("locked busy replay %+v %v %v", replay, created, err)
	}
}

func TestScanConcurrentHTTPReplayReturnsOneTask(t *testing.T) {
	_, ts, token := asyncScanFixture(t)
	type response struct {
		status int
		task   scanTaskView
		err    error
	}
	const count = 12
	start := make(chan struct{})
	out := make(chan response, count)
	for i := 0; i < count; i++ {
		go func() {
			<-start
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/tasks/scan?mode=incremental", nil)
			if err != nil {
				out <- response{err: err}
				return
			}
			req.Header.Set("X-Emby-Token", token)
			req.Header.Set("Idempotency-Key", "parallel-http-scan")
			resp, err := ts.Client().Do(req)
			if err != nil {
				out <- response{err: err}
				return
			}
			defer resp.Body.Close()
			var task scanTaskView
			err = json.NewDecoder(resp.Body).Decode(&task)
			out <- response{status: resp.StatusCode, task: task, err: err}
		}()
	}
	close(start)
	var taskID int64
	for i := 0; i < count; i++ {
		resp := <-out
		if resp.err != nil || resp.status != 202 || resp.task.TaskID <= 0 {
			t.Fatalf("parallel response %+v", resp)
		}
		if taskID == 0 {
			taskID = resp.task.TaskID
		}
		if resp.task.TaskID != taskID {
			t.Fatalf("parallel duplicate: %d != %d", resp.task.TaskID, taskID)
		}
	}
	waitScanTask(t, ts, token, taskID)
}

func TestScanPartialFailureAndUnchangedCache(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	libs, _ := app.db.Libraries()
	if _, err := app.db.AddLibrary("Unavailable", filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	app.cache.Set("scan-test-sentinel", []byte("old"), time.Hour)
	_, created := submitScan(t, ts, token, "?mode=incremental", "partial")
	done := waitScanTask(t, ts, token, created.TaskID)
	if done.Status != "failed" || done.Result == nil || done.Result.Added != 1 || !done.Result.IndexChanged || !done.Result.Incomplete || done.Result.GroupsDone < 1 {
		t.Fatalf("partial result %+v", done)
	}
	if _, ok := app.cache.Get("scan-test-sentinel"); ok {
		t.Fatal("partial commit retained stale cache")
	}
	app.cache.Set("scan-test-sentinel", []byte("warm"), time.Hour)
	_, created = submitScan(t, ts, token, fmt.Sprintf("?mode=incremental&library_id=%d", libs[0].ID), "unchanged-cache")
	done = waitScanTask(t, ts, token, created.TaskID)
	if done.Status != "succeeded" || done.Result == nil || done.Result.UnchangedGroups != 1 || done.Result.IndexChanged || !done.Result.CleanupPerformed {
		t.Fatalf("unchanged result %+v", done)
	}
	if _, ok := app.cache.Get("scan-test-sentinel"); !ok {
		t.Fatal("unchanged scan cleared warm cache")
	}
}

func TestScanRootMarkerValidationAndIdempotency(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	libs, _ := app.db.Libraries()
	root := libs[0].Path
	content := []byte("expected-library-root")
	if err := os.WriteFile(filepath.Join(root, ".marker"), content, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	params := url.Values{"mode": {"incremental"}, "root_marker_path": {".marker"}, "root_marker_sha256": {strings.ToUpper(digest)}}
	resp, created := submitScan(t, ts, token, "?"+params.Encode(), "marker-key")
	if resp.StatusCode != 202 {
		t.Fatalf("marker submit %d", resp.StatusCode)
	}
	if done := waitScanTask(t, ts, token, created.TaskID); done.Status != "succeeded" {
		t.Fatalf("marker scan %+v", done)
	}
	params.Set("root_marker_sha256", digest)
	if resp, replay := submitScan(t, ts, token, "?"+params.Encode(), "marker-key"); resp.StatusCode != 202 || replay.TaskID != created.TaskID {
		t.Fatalf("canonical marker replay %d %+v", resp.StatusCode, replay)
	}
	params.Set("root_marker_path", ".other-marker")
	if resp, _ := submitScan(t, ts, token, "?"+params.Encode(), "marker-key"); resp.StatusCode != 409 {
		t.Fatalf("marker conflict %d", resp.StatusCode)
	}
	params.Set("root_marker_path", ".marker")
	if err := os.WriteFile(filepath.Join(root, ".marker"), []byte("different-root"), 0600); err != nil {
		t.Fatal(err)
	}
	_, changed := submitScan(t, ts, token, "?"+params.Encode(), "marker-mismatch")
	if done := waitScanTask(t, ts, token, changed.TaskID); done.Status != "failed" || done.Result == nil || !done.Result.Incomplete || done.Result.IndexChanged || done.Result.CleanupPerformed {
		t.Fatalf("marker mismatch result %+v", done)
	}
}

func TestScheduledScanStrictRoundTripAndFullCompatibility(t *testing.T) {
	for _, params := range []map[string]any{{"mode": nil}, {"mode": 12}, {"mode": "unknown"}, {"library_id": -1}, {"library_id": 1.2}, {"library_id": "bad"}, {"root_marker_path": "marker"}, {"root_marker_path": "marker", "root_marker_sha256": false}} {
		if _, err := normalizeParams("scan", params); err == nil {
			t.Fatalf("accepted scan params %#v", params)
		}
	}
	app, _, token := asyncScanFixture(t)
	libs, _ := app.db.Libraries()
	body := `{"name":"Legacy full","type":"scan","cron":"0 2 * * *","enabled":false,"params":{"library_id":0,"future":{"flag":true},"allow_empty_removal":true}}`
	w := translationCall(app, token, "POST", "/api/admin/scheduled", body)
	if w.Code != 200 {
		t.Fatalf("create schedule %d %s", w.Code, w.Body.String())
	}
	var created scheduledView
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(created.Params), &params); err != nil {
		t.Fatal(err)
	}
	if _, present := params["mode"]; present {
		t.Fatal("legacy missing mode changed")
	}
	if params["future"] == nil {
		t.Fatal("opaque param lost")
	}
	req := httptest.NewRequest("PATCH", fmt.Sprintf("/api/admin/scheduled/%d", created.ID), strings.NewReader(body))
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", created.ConfigETag)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("etag update %d %s", response.Code, response.Body.String())
	}
	row, err := app.db.ScheduledTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if scheduledConfigETag(row) != created.ConfigETag || row.Params != created.Params {
		t.Fatalf("config etag/opaque changed %+v", row)
	}
	if err := app.runScheduledScan(scheduler.Task{Type: "scan", Params: params}); err != nil {
		t.Fatal(err)
	}
	if app.scanStatus.Mode != "full" {
		t.Fatalf("legacy mode %s", app.scanStatus.Mode)
	}
	if err := app.runScheduledScan(scheduler.Task{Type: "reindex", Params: map[string]any{"mode": "incremental", "library_id": 999999}}); err != nil {
		t.Fatal(err)
	}
	if app.scanStatus.Mode != "full" || app.scanStatus.Unchanged != 0 {
		t.Fatalf("reindex not full %+v", app.scanStatus)
	}
	markerData := []byte("scheduled-root")
	markerPath := filepath.Join(libs[0].Path, ".schedule-marker")
	if err := os.WriteFile(markerPath, markerData, 0600); err != nil {
		t.Fatal(err)
	}
	markerParams := map[string]any{"mode": "incremental", "root_marker_path": ".schedule-marker", "root_marker_sha256": fmt.Sprintf("%x", sha256.Sum256(markerData)), "future": map[string]any{"flag": true}}
	for _, kind := range []string{"scan", "reindex"} {
		normalized, err := normalizeParams(kind, markerParams)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip map[string]any
		if err := json.Unmarshal([]byte(normalized), &roundTrip); err != nil {
			t.Fatal(err)
		}
		if roundTrip["future"] == nil || roundTrip["root_marker_path"] != ".schedule-marker" {
			t.Fatalf("marker params lost %s", normalized)
		}
		if err := app.runScheduledScan(scheduler.Task{Type: kind, Params: roundTrip}); err != nil {
			t.Fatalf("scheduled marker %s: %v", kind, err)
		}
	}
	if err := os.WriteFile(markerPath, []byte("wrong-root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.runScheduledScan(scheduler.Task{Type: "scan", Params: markerParams}); !errors.Is(err, scanner.ErrRootMarker) {
		t.Fatalf("scheduled marker ignored: %v", err)
	}
	if err := os.Remove(filepath.Join(libs[0].Path, "Test Movie (2020).strm")); err != nil {
		t.Fatal(err)
	}
	if err := app.runScheduledScan(scheduler.Task{Type: "scan", Params: params}); !errors.Is(err, scanner.ErrReconcileBlocked) {
		t.Fatalf("schedule enabled empty cleanup: %v", err)
	}
	app.BeginShutdown()
	if _, err := app.scanLibraries(libs[0].ID, 0, scanner.ScanModeFull, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan ignored root cancellation: %v", err)
	}
}
