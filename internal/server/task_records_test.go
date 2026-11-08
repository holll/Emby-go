package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

// N-02：登记即落库（202 前可靠写入），执行、完成、重启中断都有可查摘要。
func TestScanTaskRecordLifecycle(t *testing.T) {
	app, _, _ := asyncScanFixture(t)
	id := app.startTask("scan")
	if _, _, err := app.registerScanTask(id, "scan", 7, scanner.ScanModeFull, false, "digest", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := app.db.TaskRecordByID(id)
	if err != nil || !ok {
		t.Fatalf("record after register: ok=%v err=%v", ok, err)
	}
	if rec.Status != "queued" || rec.LibraryID != 7 || rec.Type != "scan" {
		t.Fatalf("unexpected record after register: %+v", rec)
	}
	app.setScanTaskRunning(id)
	rec, _, _ = app.db.TaskRecordByID(id)
	if rec.Status != "running" || rec.StartedAt == "" {
		t.Fatalf("record after start: %+v", rec)
	}
	result := &scanner.Result{Added: 2, Updated: 1}
	app.finishScanTask(id, "succeeded", result, nil)
	rec, _, _ = app.db.TaskRecordByID(id)
	if rec.Status != "succeeded" || rec.FinishedAt == "" || rec.ResultJSON == "" {
		t.Fatalf("record after finish: %+v", rec)
	}
	var stored scanner.Result
	if err := json.Unmarshal([]byte(rec.ResultJSON), &stored); err != nil || stored.Added != 2 {
		t.Fatalf("result json = %q err=%v", rec.ResultJSON, err)
	}
}

// 重启后：内存任务丢失，列表与详情回落到摘要，未完成任务显示 interrupted。
func TestTaskCenterFallsBackToRecordsAfterRestart(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	interruptedID := int64(9001)
	if err := app.db.UpsertTaskRecord(store.TaskRecord{
		ID: interruptedID, Type: "scan", Status: "running", LibraryID: 3,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.MarkInterruptedTaskRecords(time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	get := func(path string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("X-Emby-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	code, raw := get("/api/admin/tasks")
	if code != 200 {
		t.Fatalf("tasks status %d: %s", code, raw)
	}
	var list struct {
		Items   []task `json:"items"`
		Running bool   `json:"running"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.Items {
		if item.ID == interruptedID {
			found = true
			if item.Status != "interrupted" || item.Error == "" {
				t.Fatalf("interrupted row = %+v", item)
			}
		}
	}
	if !found || list.Running {
		t.Fatalf("list missing interrupted row or running=true: %+v", list)
	}

	code, raw = get("/api/admin/tasks/" + strconv.FormatInt(interruptedID, 10))
	if code != 200 {
		t.Fatalf("detail status %d: %s", code, raw)
	}
	var detail asyncScanTask
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Status != "interrupted" || detail.LibraryID != 3 || detail.Type != "scan" {
		t.Fatalf("detail = %+v", detail)
	}
}

// 任务中心页面要显示执行结果摘要：列表把内存任务的 result 回填，且不暴露原始 result_json。
func TestTaskListIncludesResultSummary(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	id := app.startTask("scan")
	if _, _, err := app.registerScanTask(id, "scan", 7, scanner.ScanModeFull, false, "digest", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	app.finishScanTask(id, "succeeded", &scanner.Result{Added: 2, Updated: 1}, nil)

	resp, body := embyRaw(t, ts, http.MethodGet, "/api/admin/tasks", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tasks status %d: %s", resp.StatusCode, body)
	}
	var list struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		var gotID int64
		if err := json.Unmarshal(item["id"], &gotID); err != nil || gotID != id {
			continue
		}
		var result scanner.Result
		if err := json.Unmarshal(item["result"], &result); err != nil {
			t.Fatalf("result = %s err=%v", item["result"], err)
		}
		if result.Added != 2 || result.Updated != 1 {
			t.Fatalf("result = %+v", result)
		}
		if _, ok := item["result_json"]; ok {
			t.Fatal("result_json should not be exposed")
		}
		return
	}
	t.Fatalf("task %d missing from list", id)
}
