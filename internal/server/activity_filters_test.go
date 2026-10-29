package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestActivityLoginFiltersValidationAndQueries(t *testing.T) {
	for _, q := range []string{"ip=not-an-ip", "ip=127.0.0.1%2F8", "ip=fe80::1%25eth0", "result=no", "ip=127.0.0.1&ip=127.0.0.2", "result=success&result=failed"} {
		query, _ := url.ParseQuery(q)
		if _, _, err := loginActivityFilters(query); err == nil {
			t.Fatalf("accepted %s", q)
		}
	}
	f := artworkFixture(t)
	// GET filtering must reach rows outside the newest LIMIT window.
	for _, event := range []struct {
		ip      string
		success bool
	}{{"203.0.113.9", false}, {"2001:db8::9", true}, {"198.51.100.8", true}} {
		if err := f.a.db.RecordLoginEvent(1, "owner", event.ip, event.success, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"ip=203.0.113.9&result=failed", "ip=2001%3Adb8%3A%3A9&result=success", "ip=%3A%3Affff%3A203.0.113.9&result=failed"} {
		w := translationCall(f.a, f.token, "GET", "/api/admin/login-events?limit=1&"+query, "")
		var body struct {
			Events []store.LoginEvent `json:"events"`
			Total  int                `json:"total"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Total != 1 || len(body.Events) != 1 {
			t.Fatalf("query %s: %d %s", query, w.Code, w.Body.String())
		}
		if strings.Contains(query, "failed") && body.Events[0].Success {
			t.Fatal("result filter ignored")
		}
	}
	if w := translationCall(f.a, f.token, "GET", "/api/admin/login-events?ip=192.0.2.99", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"events":[]`) {
		t.Fatalf("empty %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{"ip=x", "result=x", "result=failed&result=success"} {
		if w := translationCall(f.a, f.token, "GET", "/api/admin/login-events?"+query, ""); w.Code != 400 {
			t.Fatalf("invalid %s => %d", query, w.Code)
		}
	}
	if w := translationCall(f.a, "", "GET", "/api/admin/login-events?result=all", ""); w.Code != 401 {
		t.Fatalf("unauthenticated %d", w.Code)
	}
	reader, err := f.a.db.CreateUser("activity-reader", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("activity-reader", reader.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if w := translationCall(f.a, "activity-reader", "GET", "/api/admin/login-events?result=failed", ""); w.Code != 403 {
		t.Fatalf("ordinary reader %d", w.Code)
	}
}
