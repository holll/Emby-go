package server

import (
	"strconv"
	"testing"
	"time"
)

func TestEditRecoveryListAndClearOnSuccess(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), patchFixture)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(media + "/ABF-018.strm")
	if err != nil {
		t.Fatal(err)
	}
	list := func() map[string]any {
		t.Helper()
		resp, body := embyCall(t, ts, "GET", "/api/admin/recovery", token, "")
		if resp.StatusCode != 200 {
			t.Fatalf("recovery list=%d %+v", resp.StatusCode, body)
		}
		return body
	}
	if body := list(); body["total"] != float64(0) {
		t.Fatalf("unexpected initial failures: %+v", body)
	}
	// 8 天前的失败既出现在列表里，也计入「超过 7 天」提示。
	if err := app.db.MarkEditFailure(id, "NFO_READ_FAILED", time.Now().Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	body := list()
	if body["total"] != float64(1) || body["stale_over_7d"] != float64(1) {
		t.Fatalf("failure list=%+v", body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%v", items)
	}
	item, _ := items[0].(map[string]any)
	if item["movie_id"] != float64(id) || item["code"] != "NFO_READ_FAILED" || item["age_days"].(float64) < 8 {
		t.Fatalf("item=%+v", item)
	}
	// 成功重新索引后清除待恢复项。
	path := "/api/admin/items/" + strconv.FormatInt(id, 10)
	resp, patch := embyCall(t, ts, "PATCH", path, token, `{"plot":"Recovered"}`)
	if resp.StatusCode != 200 || patch["indexed"] != true {
		t.Fatalf("patch=%d %+v", resp.StatusCode, patch)
	}
	if body := list(); body["total"] != float64(0) || body["stale_over_7d"] != float64(0) {
		t.Fatalf("failures not cleared: %+v", body)
	}
}

// DB-10：恢复列表同时暴露损坏 JSON 定位与启动回填进度（形状稳定，计数为 0 时也返回空数组）。
func TestRecoveryReportsCorruptJSONAndBackfillStatus(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), patchFixture)
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	resp, body := embyCall(t, ts, "GET", "/api/admin/recovery", token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("recovery=%d %+v", resp.StatusCode, body)
	}
	if body["corrupt_json_total"] != float64(0) {
		t.Fatalf("corrupt_json_total=%v", body["corrupt_json_total"])
	}
	if _, ok := body["corrupt_json"].([]any); !ok {
		t.Fatalf("corrupt_json 应为数组: %T", body["corrupt_json"])
	}
	_, settings := embyCall(t, ts, "GET", "/api/admin/settings", token, "")
	bf, ok := settings["features_backfill"].(map[string]any)
	if !ok {
		t.Fatalf("features_backfill 缺失: %+v", settings["features_backfill"])
	}
	for _, key := range []string{"running", "done", "total"} {
		if _, ok := bf[key]; !ok {
			t.Fatalf("features_backfill 缺 %q: %+v", key, bf)
		}
	}
}
