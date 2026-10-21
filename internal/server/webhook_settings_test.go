package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/webhook"
	"github.com/gin-gonic/gin"
)

func webhookSettingsCall(a *App, method, token, etag, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/webhook/settings", strings.NewReader(body))
	r.Header.Set("X-Emby-Token", token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", etag)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func webhookSettingsTag(t *testing.T, a *App) string {
	t.Helper()
	w := webhookSettingsCall(a, "GET", "fixture-admin-delete", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var view struct {
		ConfigETag string `json:"config_etag"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.ConfigETag == "" {
		t.Fatal(w.Body.String(), err)
	}
	return view.ConfigETag
}
func webhookSettingsBody(url string, enabled bool, events ...string) string {
	raw, _ := json.Marshal(map[string]any{"enabled": enabled, "url": url, "events": events, "format": "json", "timeout_seconds": 1, "allow_private": true, "confirm_delete": false})
	return string(raw)
}

func TestWebhookSettingsAuthorizationPersistenceAndImmediateDelivery(t *testing.T) {
	f := newAdminDeleteFixture(t)
	var mu sync.Mutex
	var received []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e webhook.Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		mu.Lock()
		received = append(received, e.Event)
		mu.Unlock()
		w.Write([]byte("ok"))
	}))
	defer receiver.Close()
	secretURL := receiver.URL + "/webhook/secret-url-canary?key=query-canary"
	user, err := f.a.db.CreateUser("ordinary", "test-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("ordinary-token", user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		for token, want := range map[string]int{"": 401, "ordinary-token": 403} {
			w := webhookSettingsCall(f.a, method, token, "bad", webhookSettingsBody(secretURL, true, "item.rate"))
			if w.Code != want {
				t.Fatalf("%s %d %s", method, w.Code, w.Body.String())
			}
		}
	}
	tag := webhookSettingsTag(t, f.a)
	if w := webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", "", webhookSettingsBody(secretURL, true, "item.rate")); w.Code != 428 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, webhookSettingsBody(secretURL, true, "item.rate"))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, value := range []string{secretURL, "secret-url-canary", "query-canary"} {
		if strings.Contains(w.Body.String(), value) {
			t.Fatal("secret echoed", w.Body.String())
		}
	}
	tag = webhookSettingsTag(t, f.a)
	w = webhookSettingsCall(f.a, "GET", "fixture-admin-delete", "", "")
	if !strings.Contains(w.Body.String(), `"url_configured":true`) || strings.Contains(w.Body.String(), "secret-url-canary") {
		t.Fatal(w.Body.String())
	}
	call := func(a *App) {
		t.Helper()
		r := httptest.NewRequest("POST", fmt.Sprintf("/Users/1/FavoriteItems/%d", f.victim), nil)
		r.Header.Set("X-Emby-Token", "fixture-admin-delete")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("X-Webhook-Status") != "sent" {
			t.Fatal(w.Code, w.Body.String(), w.Header())
		}
	}
	call(f.a)
	// Empty URL preserves the secret rather than replacing it with a UI mask.
	w = webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, webhookSettingsBody("", true, "item.rate"))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.a.Close()
	reopened, err := newApp(f.a.cfg, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.startupWG.Wait()
	call(reopened)
	tag = webhookSettingsTag(t, reopened)
	w = webhookSettingsCall(reopened, "PUT", "fixture-admin-delete", tag, `{"enabled":false,"clear_url":true,"events":["item.rate"],"format":"form","timeout_seconds":2,"allow_private":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"url_configured":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	reopened.Close()
	again, err := newApp(f.a.cfg, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.webhookSender().Subscribed("item.rate") || again.webhookConfig.URL != "" {
		t.Fatal("cleared config resurrected")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatal(received)
	}
}

func TestWebhookSettingsInvalidConflictAndFailedSavePreserveActiveConfig(t *testing.T) {
	f := newAdminDeleteFixture(t)
	tag := webhookSettingsTag(t, f.a)
	valid := webhookSettingsBody("https://receiver.example/secret-config-canary", true, "item.updated")
	for _, body := range []string{
		`{}`, `null`, valid + ` {}`,
		strings.Replace(valid, `"item.updated"`, `"unknown"`, 1),
		strings.Replace(valid, `"item.updated"`, `"item.deleted"`, 1),
		strings.Replace(valid, `"timeout_seconds":1`, `"timeout_seconds":31`, 1),
		strings.Replace(valid, `"format":"json"`, `"format":"bogus"`, 1),
		strings.Replace(valid, `"url":"https://receiver.example/secret-config-canary"`, `"url":"file:///secret-config-canary"`, 1),
		strings.Replace(valid, `"events":["item.updated"]`, `"events":[]`, 1),
		strings.Replace(valid, `"allow_private":true`, `"allow_private":false`, 1) + `x`,
		strings.Replace(valid, `"enabled":true`, `"enabled":null`, 1),
	} {
		w := webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, body)
		if w.Code != 400 || strings.Contains(w.Body.String(), "secret-config-canary") {
			t.Fatalf("body=%s status=%d response=%s", body, w.Code, w.Body.String())
		}
		if got := webhookSettingsTag(t, f.a); got != tag {
			t.Fatal("invalid config changed current")
		}
	}
	if _, err := f.db.Exec(`CREATE TRIGGER reject_webhook BEFORE INSERT ON settings WHEN NEW.key='webhook.config.v1' BEGIN SELECT RAISE(ABORT,'private-sql-canary'); END`); err != nil {
		t.Fatal(err)
	}
	old := f.a.webhookSender()
	w := webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, valid)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private-sql-canary") || f.a.webhookSender() != old || webhookSettingsTag(t, f.a) != tag {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, err := f.a.db.Setting(settingWebhook)
	if err != nil || raw != "" {
		t.Fatal(raw, err)
	}
	if _, err = f.db.Exec(`DROP TRIGGER reject_webhook`); err != nil {
		t.Fatal(err)
	}
	// Two editors with the same original token cannot overwrite each other.
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for _, event := range []string{"item.rate", "item.updated"} {
		go func(event string) {
			<-start
			statuses <- webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, webhookSettingsBody("https://receiver.example/secret-config-canary", true, event)).Code
		}(event)
	}
	close(start)
	a, b := <-statuses, <-statuses
	if !((a == 200 && b == 412) || (a == 412 && b == 200)) {
		t.Fatal(a, b)
	}
	if _, err = f.db.Exec(`UPDATE settings SET value='corrupt-secret-canary' WHERE key=?`, settingWebhook); err != nil {
		t.Fatal(err)
	}
	f.a.Close()
	app, err := newApp(f.a.cfg, cache.NewMemory(16))
	if err == nil {
		app.Close()
		t.Fatal("corrupt persistent config accepted")
	}
	if strings.Contains(err.Error(), "corrupt-secret-canary") {
		t.Fatal(err)
	}
}

func TestWebhookSettingsSwapDoesNotWaitForInFlightDelivery(t *testing.T) {
	f := newAdminDeleteFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	oldReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.Write([]byte("ok")) }))
	defer func() { once.Do(func() { close(release) }); oldReceiver.Close() }()
	nextCount := make(chan struct{}, 1)
	newReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCount <- struct{}{}; w.Write([]byte("ok")) }))
	defer newReceiver.Close()
	save := func(url string) *httptest.ResponseRecorder {
		return webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", webhookSettingsTag(t, f.a), webhookSettingsBody(url, true, "item.rate"))
	}
	if w := save(oldReceiver.URL); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		f.a.notifyWebhook(c, webhook.Event{Event: "item.rate", Item: webhook.Item{Id: "1", Path: "/movie.strm"}})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old receiver not entered")
	}
	saved := make(chan *httptest.ResponseRecorder, 1)
	go func() { saved <- save(newReceiver.URL) }()
	select {
	case w := <-saved:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("save blocked on old HTTP delivery")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	f.a.notifyWebhook(c, webhook.Event{Event: "item.rate", Item: webhook.Item{Id: "1", Path: "/movie.strm"}})
	select {
	case <-nextCount:
	default:
		t.Fatal("new sender not used")
	}
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("old delivery did not finish")
	}
}
