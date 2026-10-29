package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Use the actual Handler/authentication/middleware rather than invoking the
// dashboard handler directly. All data and credentials live in a temporary DB.
func TestDashboardRealHTTPContractAndSampling(t *testing.T) {
	a, _, _ := authorityApp(t)
	seedOwner(t, a)
	if err := a.db.SaveAccessTokenForUser("dash-owner-token", 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	viewer, err := a.db.CreateUser("dash-viewer", "dash-viewer-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.SaveAccessTokenForUser("dash-viewer-token", viewer.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	get := func(route, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", route, nil)
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s got %d: %s", route, w.Code, w.Body.String())
		}
		return w
	}
	snapshot := func() map[string]json.RawMessage {
		t.Helper()
		w := get("/api/admin/dashboard?window=60m", "dash-owner-token", 200)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("dashboard must not be cached", w.Header())
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		var points []json.RawMessage
		if err := json.Unmarshal(data["series"], &points); err != nil || len(points) != 60 {
			t.Fatal("invalid minute series", err, len(points))
		}
		return data
	}
	counts := func(data map[string]json.RawMessage) map[string]float64 {
		t.Helper()
		var value map[string]any
		if err := json.Unmarshal(data["requests"], &value); err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, key := range []string{"total", "errors_4xx", "errors_5xx"} {
			n, ok := value[key].(float64)
			if !ok {
				t.Fatalf("missing %s: %v", key, value)
			}
			out[key] = n
		}
		return out
	}
	get("/api/admin/dashboard", "", 401)
	get("/api/admin/dashboard", "dash-viewer-token", 403)
	before := counts(snapshot())
	for _, query := range []string{"?window=", "?window=1m", "?window=5m&window=15m"} {
		get("/api/admin/dashboard"+query, "dash-owner-token", 400)
	}
	for _, resource := range []string{"/web/dashboard.js", "/web/dashboard.css", "/api/admin/status"} {
		get(resource, "dash-owner-token", 200)
	}
	for range 3 {
		snapshot()
	}
	if got := counts(snapshot()); got["total"] != before["total"] {
		t.Fatalf("monitor/static/poll traffic fed its own metrics: before=%v after=%v", before, got)
	}
	get("/api/admin/libraries?query=dash-private-query-value", "dash-owner-token", 200)
	get("/Items/999999999", "dash-owner-token", 404)
	after := counts(snapshot())
	if after["total"] != before["total"]+2 || after["errors_4xx"] != before["errors_4xx"]+1 {
		t.Fatalf("control requests not sampled exactly once: before=%v after=%v", before, after)
	}
	const secret = "dash-private-error-/home/media/secret.mkv"
	taskID := a.startTask(secret)
	a.finishTask(taskID, errors.New(secret))
	safe := get("/api/admin/dashboard", "dash-owner-token", 200).Body.String()
	for _, forbidden := range []string{secret, "dash-private-query-value", "dash-owner-token", "dash-viewer-token", "/home/media/secret.mkv"} {
		if strings.Contains(safe, forbidden) {
			t.Fatalf("private request/task material leaked: %q", forbidden)
		}
	}
	var data struct {
		Tasks struct {
			Recent []map[string]any `json:"recent"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(safe), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Tasks.Recent) != 1 {
		t.Fatalf("tasks=%v", data.Tasks.Recent)
	}
	for key := range data.Tasks.Recent[0] {
		switch key {
		case "id", "kind", "status", "started_at", "ended_at":
		default:
			t.Fatalf("non-whitelisted task property %q", key)
		}
	}
}
