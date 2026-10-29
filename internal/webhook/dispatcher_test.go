package webhook

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testTarget(id, url string, events ...string) Target {
	return Target{ID: id, Name: id, Enabled: true, URL: url, Events: events, Format: "json",
		TimeoutSeconds: 1, AllowPrivate: true}
}

func TestDispatcherPartialDeliveryAndIndependentTargets(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer okServer.Close()
	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer badServer.Close()
	d, err := NewDispatcher([]Target{
		testTarget("wh_ok", okServer.URL, ItemDeleted),
		testTarget("wh_bad", badServer.URL, ItemDeleted),
		{ID: "wh_off", Name: "off", Enabled: false, Events: []string{ItemDeleted}, Format: "json", TimeoutSeconds: 1},
	}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	event := Event{Event: ItemDeleted, Item: Item{Id: "1", Path: "/media/x.strm"}}
	result := d.Send(context.Background(), event)
	if result.Status != ResultPartial || len(result.Targets) != 2 {
		t.Fatalf("result=%+v", result)
	}
	byID := map[string]TargetOutcome{}
	for _, outcome := range result.Targets {
		byID[outcome.ID] = outcome
	}
	if byID["wh_ok"].Status != TargetSent || byID["wh_bad"].Status != TargetFailed || byID["wh_bad"].ErrorCode != "status_500" {
		t.Fatalf("outcomes=%+v", result.Targets)
	}
}

func TestDispatcherDisabledAndTotalBudget(t *testing.T) {
	if d, err := NewDispatcher(nil, DefaultEventBudget); err != nil || d.Subscribed(ItemDeleted) {
		t.Fatalf("empty dispatcher err=%v", err)
	}
	d, err := NewDispatcher([]Target{testTarget("wh_slow", "http://127.0.0.1:1", ItemDeleted)}, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// 没有匹配事件时不该触发任何发送。
	if result := d.Send(context.Background(), Event{Event: UserLogout}); result.Status != ResultDisabled || len(result.Targets) != 0 {
		t.Fatalf("disabled result=%+v", result)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte("late"))
	}))
	defer slow.Close()
	slowD, err := NewDispatcher([]Target{testTarget("wh_slow", slow.URL, ItemDeleted)}, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer slowD.Close()
	start := time.Now()
	result := slowD.Send(context.Background(), Event{Event: ItemDeleted, Item: Item{Path: "/media/x.strm"}})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("budget exceeded: %s", elapsed)
	}
	if result.Status != ResultFailed || len(result.Targets) != 1 || result.Targets[0].ErrorCode == "" {
		t.Fatalf("budget result=%+v", result)
	}
}

func TestDispatcherValidationLimits(t *testing.T) {
	var targets []Target
	for i := range MaxTargets + 1 {
		targets = append(targets, testTarget(fmt.Sprintf("wh_%02d", i), "", "item.updated"))
	}
	if _, err := NewDispatcher(targets, DefaultEventBudget); err == nil {
		t.Fatal("expected too many targets")
	}
	cases := []Target{
		{ID: "wh_1", Name: "n", Enabled: true, URL: "https://example.com", Events: nil},
		{ID: "wh_2", Name: "n", Enabled: true, URL: "https://example.com", Events: []string{"*"}},
		{ID: "wh_3", Name: "n", Enabled: true, URL: "https://example.com", Events: []string{ItemUpdated}, Format: "xml"},
		{ID: "wh_4", Name: "n", Enabled: true, Events: []string{ItemUpdated}},
		{ID: "wh_5", Name: "n", Enabled: true, URL: "file:///etc/passwd", Events: []string{ItemUpdated}},
	}
	for _, target := range cases {
		if _, err := NewDispatcher([]Target{target}, DefaultEventBudget); err == nil {
			t.Fatalf("accepted invalid target %+v", target)
		}
	}
	if id, err := NewTargetID(); err != nil || !strings.HasPrefix(id, "wh_") || len(id) != 19 {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestDispatcherSendIgnoresDisabledTargetSlot(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer target.Close()
	d, err := NewDispatcher([]Target{testTarget("wh_a", target.URL, ItemDeleted)}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !d.Subscribed(ItemDeleted) || d.Subscribed(ItemUpdated) {
		t.Fatal("subscription mismatch")
	}
}
