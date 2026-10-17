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
