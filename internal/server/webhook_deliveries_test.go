package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"emby-go/internal/webhook"
)

func TestAdminWebhookDeliveriesListAndClear(t *testing.T) {
	app, ts, token := contractEnv(t)
	app.recordWebhookDeliveries(webhook.Event{Event: "item.updated", Item: webhook.Item{Id: "42"}}, webhook.Result{
		Status: webhook.ResultPartial,
		Targets: []webhook.TargetOutcome{
			{ID: "wh_1", Status: webhook.TargetSent},
			{ID: "wh_2", Status: webhook.TargetFailed, ErrorCode: "timeout"},
		},
	})
	call := func(method, path string) (int, []byte) {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.Header.Set("X-Emby-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	status, raw := call(http.MethodGet, "/api/admin/webhook/deliveries")
	if status != 200 {
		t.Fatalf("list status %d: %s", status, raw)
	}
	var got struct {
		Items []struct {
			Event     string `json:"event"`
			ItemID    string `json:"item_id"`
			TargetID  string `json:"target_id"`
			Status    string `json:"status"`
			ErrorCode string `json:"error_code"`
			CreatedAt string `json:"created_at"`
		} `json:"items"`
		Retention struct {
			MaxRows int `json:"max_rows"`
			Days    int `json:"days"`
		} `json:"retention"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].TargetID != "wh_2" || got.Items[0].Status != "failed" || got.Items[0].ErrorCode != "timeout" || got.Items[0].ItemID != "42" || got.Items[0].Event != "item.updated" {
		t.Fatalf("unexpected newest item: %+v", got.Items[0])
	}
	if got.Retention.MaxRows != 1000 || got.Retention.Days != 7 {
		t.Fatalf("retention = %+v", got.Retention)
	}
	if status, _ := call(http.MethodGet, "/api/admin/webhook/deliveries?limit=0"); status != 400 {
		t.Fatalf("limit=0 status %d", status)
	}
	if status, raw := call(http.MethodDelete, "/api/admin/webhook/deliveries"); status != 200 {
		t.Fatalf("clear status %d: %s", status, raw)
	}
	status, raw = call(http.MethodGet, "/api/admin/webhook/deliveries")
	if status != 200 {
		t.Fatalf("list after clear %d: %s", status, raw)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("items after clear = %+v", got.Items)
	}
}

func TestAdminWebhookDeliveriesRejectsNonAdmin(t *testing.T) {
	_, ts, adminToken := contractEnv(t)
	viewer := viewerToken(t, ts, adminToken)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(method, ts.URL+"/api/admin/webhook/deliveries", nil)
		req.Header.Set("X-Emby-Token", viewer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s as viewer = %d, want 403", method, resp.StatusCode)
		}
	}
}
