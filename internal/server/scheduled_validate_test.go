package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validateCron(t *testing.T, a *App, token, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/scheduled/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return w.Code, out
}

func TestCronValidateReturnsTimeZoneAndPreview(t *testing.T) {
	a := ttlApp(t, 30)
	token := loginForToken(t, a)
	code, out := validateCron(t, a, token, `{"cron":"0 3 * * *"}`)
	if code != http.StatusOK || out["valid"] != true {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if tz, _ := out["time_zone"].(string); tz == "" {
		t.Fatalf("缺少 time_zone: %v", out)
	}
	runs, _ := out["next_runs"].([]any)
	if len(runs) != 5 {
		t.Fatalf("next_runs=%v", out["next_runs"])
	}
}

func TestCronValidateRejectsInvalidExpression(t *testing.T) {
	a := ttlApp(t, 30)
	token := loginForToken(t, a)
	for _, body := range []string{`{"cron":"not a cron"}`, `{"cron":""}`} {
		code, out := validateCron(t, a, token, body)
		if code != http.StatusOK || out["valid"] != false {
			t.Fatalf("body=%s code=%d out=%v", body, code, out)
		}
		if msg, _ := out["error"].(string); msg == "" {
			t.Fatalf("缺少 error: %v", out)
		}
	}
}
