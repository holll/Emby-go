package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func conditionalScheduledCall(t *testing.T, ts *httptest.Server, path, auth, etag, body string, methods ...string) (int, map[string]any) {
	t.Helper()
	method := "PATCH"
	if len(methods) != 0 {
		method = methods[0]
	}
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", auth)
	req.Header.Set("If-Match", etag)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, result
}

func TestScheduledConditionalEditCompatibility(t *testing.T) {
	app, _ := detailTestApp(t, t.TempDir(), `<movie><title>Fixture</title></movie>`)
	ts, auth := detailServer(t, app)
	body := func(name string) string {
		return `{"name":"` + name + `","type":"probe","cron":"0 3 * * *","enabled":false,"params":{"library_id":0,"limit":50,"only_missing":false}}`
	}
	resp, created := embyCall(t, ts, "POST", "/api/admin/scheduled", auth, body("Original"))
	if resp.StatusCode != 200 {
		t.Fatal(created)
	}
	id := int64(created["id"].(float64))
	path := "/api/admin/scheduled/" + strconv.FormatInt(id, 10)
	tag, ok := created["config_etag"].(string)
	if !ok || tag == "" {
		t.Fatal(created)
	}
	_, listed := embyCall(t, ts, "GET", "/api/admin/scheduled", auth, "")
	item := listed["items"].([]any)[0].(map[string]any)
	if item["config_etag"] != tag {
		t.Fatal("list validator differs")
	}
	missingResp, missing := embyCall(t, ts, "PATCH", path, auth, body("Missing"))
	if missingResp.StatusCode != 428 || missing["code"] != "SCHEDULED_PRECONDITION_REQUIRED" {
		t.Fatalf("missing condition: %d %+v", missingResp.StatusCode, missing)
	}
	putStatus, putResult := conditionalScheduledCall(t, ts, path, auth, tag, body("Original"), "PUT")
	if putStatus != 200 || putResult["config_etag"] != tag {
		t.Fatalf("conditional PUT compatibility: %d %+v", putStatus, putResult)
	}
	status, updated := conditionalScheduledCall(t, ts, path, auth, tag, body("A"))
	if status != 200 || updated["config_etag"] == tag {
		t.Fatalf("update=%d %+v", status, updated)
	}
	tagA := updated["config_etag"].(string)
	for _, stale := range []string{tag, "", "*", `W/` + tagA, tagA + ", " + tag} {
		status, result := conditionalScheduledCall(t, ts, path, auth, stale, body("B"))
		if status != 412 || result["code"] != "SCHEDULED_CONFIG_CHANGED" {
			t.Fatalf("stale=%q %d %+v", stale, status, result)
		}
	}
	current, err := app.db.ScheduledTask(id)
	if err != nil || current.Name != "A" || !strings.Contains(current.Params, `"limit":50`) {
		t.Fatalf("stale write won: %+v %v", current, err)
	}
	if err := app.db.RecordScheduledResult(id, "skipped", "fixture-result"); err != nil {
		t.Fatal(err)
	}
	status, updated = conditionalScheduledCall(t, ts, path, auth, tagA, body("After run"))
	if status != 200 || updated["last_status"] != "skipped" {
		t.Fatalf("runtime invalidated edit: %d %+v", status, updated)
	}
	oldTag := updated["config_etag"].(string)
	// An unchanged legacy client may still issue an unconditional PUT.
	resp, legacy := embyCall(t, ts, "PUT", path, auth, body("Legacy"))
	if resp.StatusCode != 200 || legacy["name"] != "Legacy" {
		t.Fatal(legacy)
	}
	status, _ = conditionalScheduledCall(t, ts, path, auth, oldTag, body("Stale after legacy"))
	if status != 412 {
		t.Fatal(status)
	}
	status, _ = conditionalScheduledCall(t, ts, path, "", legacy["config_etag"].(string), body("Unauth"))
	if status != 401 {
		t.Fatal(status)
	}
	// DB state changes between UI load and save invalidate the enabled checkbox.
	if err := app.db.SetScheduledTaskEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	status, _ = conditionalScheduledCall(t, ts, path, auth, legacy["config_etag"].(string), body("Stale disabled"))
	if status != 412 {
		t.Fatal(status)
	}
	current, _ = app.db.ScheduledTask(id)
	if current.Name != "Legacy" || !current.Enabled {
		t.Fatalf("stale checkbox overwrote toggle: %+v", current)
	}
	// No enabled definition was reloaded into the test scheduler; restore it.
	if err := app.db.SetScheduledTaskEnabled(id, false); err != nil {
		t.Fatal(err)
	}
	if err := app.db.DeleteScheduledTask(id); err != nil {
		t.Fatal(err)
	}
	status, _ = conditionalScheduledCall(t, ts, path, auth, legacy["config_etag"].(string), body("Deleted"))
	if status != 412 {
		t.Fatal(status)
	}
}
