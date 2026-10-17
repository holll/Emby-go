package server

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

func TestScheduledKnownHiddenParamsRoundTrip(t *testing.T) {
	app, _ := detailTestApp(t, t.TempDir(), `<movie><title>Fixture</title></movie>`)
	ts, token := detailServer(t, app)
	for _, tc := range []struct{ kind, params string }{
		{"probe", `{"library_id":0,"probe_status_contract":"explicit-v1","status":"manual","limit":50,"only_missing":false}`},
		{"scrape", `{"library_id":0,"limit":25,"only_missing":false,"overwrite":false}`},
		{"scrape_avatars", `{"library_id":0,"limit":12}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			body := `{"name":"Original","type":"` + tc.kind + `","cron":"0 3 * * *","enabled":false,"params":` + tc.params + `}`
			resp, created := embyCall(t, ts, "POST", "/api/admin/scheduled", token, body)
			if resp.StatusCode != 200 {
				t.Fatalf("create %d %+v", resp.StatusCode, created)
			}
			id := int64(created["id"].(float64))
			before, err := app.db.ScheduledTask(id)
			if err != nil {
				t.Fatal(err)
			}
			// The UI merges saved known parameters before replacing visible form values.
			body = `{"name":"Renamed","type":"` + tc.kind + `","cron":"30 4 * * *","enabled":false,"params":` + before.Params + `}`
			resp, updated := embyCall(t, ts, "PUT", "/api/admin/scheduled/"+strconv.FormatInt(id, 10), token, body)
			if resp.StatusCode != 200 {
				t.Fatalf("update %d %+v", resp.StatusCode, updated)
			}
			after, err := app.db.ScheduledTask(id)
			if err != nil {
				t.Fatal(err)
			}
			var oldParams, newParams map[string]any
			if err := json.Unmarshal([]byte(before.Params), &oldParams); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(after.Params), &newParams); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(oldParams, newParams) || after.Name != "Renamed" || after.Cron != "30 4 * * *" {
				t.Fatalf("changed hidden settings: %s -> %s", before.Params, after.Params)
			}
			// Changing type replaces the parameter schema, not a probe/scrape hybrid.
			body = `{"name":"Changed type","type":"scan","cron":"0 3 * * *","enabled":false,"params":{"library_id":0}}`
			resp, _ = embyCall(t, ts, "PUT", "/api/admin/scheduled/"+strconv.FormatInt(id, 10), token, body)
			if resp.StatusCode != 200 {
				t.Fatal(resp.StatusCode)
			}
			changed, err := app.db.ScheduledTask(id)
			if err != nil {
				t.Fatal(err)
			}
			if changed.Type != "scan" || changed.Params != `{"library_id":0}` {
				t.Fatalf("type leak: %+v", changed)
			}
		})
	}
}
