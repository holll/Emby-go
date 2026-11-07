package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestWebhookDeliveriesRecordPruneClear(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "webhook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	old := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	if err := s.RecordWebhookDeliveries([]WebhookDelivery{
		{Event: "item.updated", ItemID: "42", TargetID: "wh_1", Status: "sent", CreatedAt: old},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.RecordWebhookDeliveries([]WebhookDelivery{
		{Event: "item.updated", ItemID: "43", TargetID: "wh_1", Status: "failed", ErrorCode: "timeout", CreatedAt: now},
		{Event: "item.updated", ItemID: "43", TargetID: "wh_2", Status: "skipped_budget", ErrorCode: "budget_exhausted", CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListWebhookDeliveries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("old row should be pruned by age: %+v", items)
	}
	if items[0].TargetID != "wh_2" || items[0].Status != "skipped_budget" || items[0].ErrorCode != "budget_exhausted" {
		t.Fatalf("unexpected newest row: %+v", items[0])
	}
	if items[1].TargetID != "wh_1" || items[1].Status != "failed" || items[1].ErrorCode != "timeout" {
		t.Fatalf("unexpected second row: %+v", items[1])
	}

	batch := make([]WebhookDelivery, 0, WebhookDeliveryRetentionRows+50)
	for i := 0; i < WebhookDeliveryRetentionRows+50; i++ {
		batch = append(batch, WebhookDelivery{Event: "playback.progress", ItemID: fmt.Sprintf("%d", i), TargetID: "wh_1", Status: "sent", CreatedAt: now})
	}
	if err := s.RecordWebhookDeliveries(batch); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListWebhookDeliveries(WebhookDeliveryRetentionRows + 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != WebhookDeliveryRetentionRows {
		t.Fatalf("count retention = %d, want %d", len(items), WebhookDeliveryRetentionRows)
	}
	removed, err := s.ClearWebhookDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	if removed != WebhookDeliveryRetentionRows {
		t.Fatalf("cleared %d rows, want %d", removed, WebhookDeliveryRetentionRows)
	}
	if items, err = s.ListWebhookDeliveries(10); err != nil || len(items) != 0 {
		t.Fatalf("after clear: %v %+v", err, items)
	}
}
