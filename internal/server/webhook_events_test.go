package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"emby-go/internal/config"
	"emby-go/internal/webhook"
)

func webhookDispatcherForTest(t *testing.T, cfg config.WebhookConfig) *webhook.Dispatcher {
	t.Helper()
	spec := legacyWebhookTargetSpec(normalizeWebhookConfig(cfg))
	dispatcher, err := webhook.NewDispatcher([]webhook.Target{spec.target()}, webhook.DefaultEventBudget)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestWebhookEventsAfterSuccessfulWrites(t *testing.T) {
	f := newAdminDeleteFixture(t)
	events := make(chan webhook.Event, 32)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e webhook.Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		events <- e
		w.Write([]byte("ok"))
	}))
	defer target.Close()
	cfg := config.WebhookConfig{Enabled: true, URL: target.URL, AllowPrivate: true, TimeoutSeconds: 1, Events: []string{"user.authenticated", "user.logout", "item.rate", "item.unrate", "item.markplayed", "item.markunplayed", "item.updated", "playback.start", "playback.progress", "playback.pause", "playback.unpause", "playback.stop"}}
	sender := webhookDispatcherForTest(t, cfg)
	defer sender.Close()
	f.a.webhook.Close()
	f.a.webhook = sender
	handler := f.a.Handler()
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Emby-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	expect := func(name string) webhook.Event {
		t.Helper()
		select {
		case e := <-events:
			if e.Event != name || e.User == nil || e.User.Id != "1" || e.Date.IsZero() || !e.Date.Equal(e.Timestamp) || e.Server.Id == "" {
				t.Fatalf("event=%+v want=%s", e, name)
			}
			raw, _ := json.Marshal(e)
			for _, secret := range []string{"fixture-admin-delete", "fixture-pass-123", "AccessToken", "Password", "https://media.test"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("payload leaked %s", secret)
				}
			}
			return e
		default:
			t.Fatalf("missing %s", name)
			return webhook.Event{}
		}
	}
	none := func() {
		t.Helper()
		select {
		case e := <-events:
			t.Fatalf("unexpected %+v", e)
		default:
		}
	}
	if w := call("POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"wrong"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	none()
	w := call("POST", "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"fixture-pass-123"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	expect("user.authenticated")
	var login struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil || login.AccessToken == "" {
		t.Fatal(err)
	}
	if w = call("POST", "/Sessions/Logout", login.AccessToken, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	expect("user.logout")
	token := "fixture-admin-delete"
	for _, tc := range []struct{ method, path, event string }{
		{"POST", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim), "item.rate"},
		{"DELETE", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim), "item.rate"},
		{"POST", fmt.Sprintf("/Users/1/Items/%d/Rating?Likes=true", f.victim), "item.rate"},
		{"DELETE", fmt.Sprintf("/Users/1/Items/%d/Rating", f.victim), "item.unrate"},
		{"POST", fmt.Sprintf("/Users/1/PlayedItems/%d", f.victim), "item.markplayed"},
		{"DELETE", fmt.Sprintf("/Users/1/PlayedItems/%d", f.victim), "item.markunplayed"},
	} {
		if w := call(tc.method, tc.path, "", ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
		none()
		w := call(tc.method, tc.path, token, "")
		if w.Code != 200 && w.Code != 204 {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
		e := expect(tc.event)
		if e.Item.Id != fmt.Sprint(f.victim) || e.Item.UserData == nil {
			t.Fatalf("missing item/data: %+v", e)
		}
	}
	if _, err := f.db.Exec(`CREATE TRIGGER event_fail BEFORE UPDATE ON userdata BEGIN SELECT RAISE(ABORT,'private-canary'); END`); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim), token, ""); w.Code != 500 {
		t.Fatal(w.Code)
	}
	none()
	if _, err := f.db.Exec("DROP TRIGGER event_fail"); err != nil {
		t.Fatal(err)
	}
	// Full indexed edit sends updated; a partial result must not claim indexed success.
	path := fmt.Sprintf("/api/admin/items/%d", f.victim)
	if w := call("PATCH", path, token, `{"title":"Webhook updated"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	e := expect("item.updated")
	if e.Item.Name != "Webhook updated" {
		t.Fatalf("stale title: %+v", e.Item)
	}
	if _, err := f.db.Exec(`CREATE TRIGGER event_index_fail BEFORE UPDATE ON movies WHEN NEW.title='Partial webhook' BEGIN SELECT RAISE(ABORT,'private-canary'); END`); err != nil {
		t.Fatal(err)
	}
	if w := call("PATCH", path, token, `{"title":"Partial webhook"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "partial_success") {
		t.Fatal(w.Code, w.Body.String())
	}
	none()
	if _, err := f.db.Exec("DROP TRIGGER event_index_fail"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ suffix, name, event string }{
		{"", "", "playback.start"}, {"/Progress", "", "playback.progress"}, {"/Progress", "Pause", "playback.pause"}, {"/Progress", "Unpause", "playback.unpause"},
	} {
		body := fmt.Sprintf(`{"ItemId":%q,"PlaySessionId":"events-session","DeviceId":"device","PositionTicks":123,"EventName":%q}`, fmt.Sprint(f.victim), tc.name)
		if w := call("POST", "/Sessions/Playing"+tc.suffix, token, body); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
		e := expect(tc.event)
		if e.Session == nil || e.PlaybackInfo == nil || e.PlaybackInfo.PositionTicks != 123 || e.Session.DeviceId != "device" {
			t.Fatalf("playback fields: %+v", e)
		}
	}
	// Commit has occurred even if the legacy, subsequent version write fails.
	if _, err := f.db.Exec(`CREATE TRIGGER event_version_fail BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'private-canary'); END`); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"ItemId":%q,"PlaySessionId":"events-session","PositionTicks":123}`, fmt.Sprint(f.victim))
	if w := call("POST", "/Sessions/Playing/Stopped", token, body); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	expect("playback.stop")
	if _, err := f.db.Exec("DROP TRIGGER event_version_fail"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/Stopped", "/Progress"} {
		if w := call("POST", "/Sessions/Playing"+suffix, token, body); w.Code != 204 {
			t.Fatal(w.Code)
		}
		none()
	}
	// Explicit selection replaces the deletion default, rather than adding to it.
	if w := call("DELETE", fmt.Sprintf("/api/admin/items/%d", f.survivor), token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	none()
}

func TestWebhookFailureDoesNotUndoOtherWrites(t *testing.T) {
	f := newAdminDeleteFixture(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer target.Close()
	s := webhookDispatcherForTest(t, config.WebhookConfig{Enabled: true, URL: target.URL, AllowPrivate: true, Events: []string{"item.rate"}})
	defer s.Close()
	f.a.webhook.Close()
	f.a.webhook = s
	r := httptest.NewRequest("POST", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim), nil)
	r.Header.Set("X-Emby-Token", "fixture-admin-delete")
	w := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, r)
	d, err := f.a.db.DataUser(1, f.victim)
	if w.Code != 200 || w.Header().Get("X-Webhook-Status") != "failed" || err != nil || !d.IsFavorite {
		t.Fatalf("status=%d data=%+v err=%v", w.Code, d, err)
	}
}
