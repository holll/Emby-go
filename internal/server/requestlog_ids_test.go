package server

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDiagnosticRouteActualIDs(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	for _, tc := range []struct{ uid, id, loggedUID, loggedID string }{
		{"1", "42", "1", "42"},
		{"23", "1000000000001", "23", "1000000000001"},
		{"1", "9223372036854775807", "1", "9223372036854775807"},
		{"1", "part-42-2", "1", "part-42-2"},
		{"1", "boxsets", "1", "boxsets"},
		{"1", "boxset:U2VyaWVzIEE", "1", "boxset:U2VyaWVzIEE"},
		{"canary-user", "42", ":uid", "42"},
		{"1", "canary-path", "1", ":id"},
		{"1", "9223372036854775808", "1", ":id"},
		{"1", "part-canary-2", "1", ":id"},
		{"1", "boxset:canary=", "1", ":id"},
		{"1", "boxset:" + strings.Repeat("A", 516), "1", ":id"},
		{"1", "canary\npath", "1", ":id"},
	} {
		t.Run(tc.uid+"/"+tc.id, func(t *testing.T) {
			for _, prefix := range []string{"", "/emby"} {
				var output bytes.Buffer
				r := gin.New()
				r.Use(requestLoggerTo(&output), requestContentLoggerTo(&output))
				target := prefix + "/Users/" + url.PathEscape(tc.uid) + "/Items/" + url.PathEscape(tc.id) + "?token=canary-query&UserId=1&ParentId=1000000000001"
				r.GET(prefix+"/Users/:uid/Items/:id", func(c *gin.Context) {
					if c.Param("uid") != tc.uid || c.Param("id") != tc.id || c.Request.RequestURI != target {
						t.Error("logging changed request")
					}
					c.Status(200)
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
				got := output.String()
				want := prefix + "/Users/" + tc.loggedUID + "/Items/" + tc.loggedID
				if w.Code != 200 || strings.Count(got, want) != 2 || strings.Contains(got, "canary") || !strings.Contains(got, "ParentId=1000000000001") || !strings.Contains(got, "UserId=1") {
					t.Fatalf("status=%d want=%s log=%s", w.Code, want, got)
				}
			}
		})
	}
}

func TestDiagnosticQueryIDs(t *testing.T) {
	safe := url.Values{"UserId": {"23"}, "ItemId": {"42"}, "ParentId": {"boxset:U2VyaWVzIEE"}, "mediaSourceId": {"part-42-2"}, "Ids": {"42,boxset:U2VyaWVzIEE"}, "IncludeItemTypes": {"BoxSet,Movie"}, "ExcludeItemTypes": {"Series"}}
	if got := redactedRequestQuery(safe.Encode()); got != safe.Encode() {
		t.Fatalf("valid IDs hidden: %s", got)
	}
	for _, key := range []string{"UserId", "ItemId", "ParentId", "mediaSourceId", "Ids", "IncludeItemTypes", "ExcludeItemTypes"} {
		for _, value := range []string{"canary", "https://canary/private?token=canary", "42\ncanary", strings.Repeat("1", 20)} {
			got := redactedRequestQuery(url.Values{key: {value}}.Encode())
			if strings.Contains(got, "canary") || !strings.Contains(got, "REDACTED") {
				t.Fatalf("unsafe %s value in %s", key, got)
			}
		}
		got := redactedRequestQuery(url.Values{key: {"1", "2"}}.Encode())
		if !strings.Contains(got, "REDACTED") {
			t.Fatalf("duplicate %s not hidden: %s", key, got)
		}
	}
}

func TestDiagnosticIDLimitsAndEncoding(t *testing.T) {
	if !diagnosticItemID("boxset:"+strings.Repeat("A", 512)) || diagnosticItemID("boxset:"+strings.Repeat("A", 513)) || diagnosticItemID("boxset:QQ\n") || diagnosticItemID("boxset:QR") {
		t.Fatal("virtual ID length/canonical encoding boundary is incorrect")
	}
	for _, bad := range []string{"1,,2", ",1", "1,", strings.Repeat("1,", 32) + "1", "1,canary", strings.Repeat("1", 2049)} {
		if diagnosticItemIDs(bad) {
			t.Errorf("invalid ID list accepted: %q", bad)
		}
	}
	if !diagnosticItemIDs(strings.Repeat("1,", 31) + "1") {
		t.Fatal("32 valid IDs rejected")
	}
	for _, raw := range []string{"%34%32", "42%250Acanary", "42%0Acanary", "42%2Fcanary"} {
		var output bytes.Buffer
		r := gin.New()
		r.Use(requestLoggerTo(&output))
		r.GET("/Items/:id", func(c *gin.Context) { c.Status(200) })
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/Items/"+raw, nil))
		got := output.String()
		if strings.Contains(got, "canary") || (raw == "%34%32" && !strings.Contains(got, "/Items/42")) {
			t.Fatalf("unsafe encoded ID: %s", got)
		}
	}
}

func TestDiagnosticActualIDsOnFailure(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		var output bytes.Buffer
		r := gin.New()
		r.Use(requestLoggerTo(&output), requestContentLoggerTo(&output), requestRecoveryTo(&output))
		r.GET("/emby/Users/:uid/Items/:id", func(c *gin.Context) {
			if status == 500 {
				panic("canary-panic")
			}
			c.AbortWithStatus(status)
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/emby/Users/1/Items/42?%61pi_key=canary-one&api_key=canary-two&Token=canary-three", nil))
		got := output.String()
		if w.Code != status || strings.Count(got, "/emby/Users/1/Items/42") != 2 || strings.Contains(got, "canary") {
			t.Fatalf("status=%d log=%s", w.Code, got)
		}
	}
}

func TestDiagnosticOnlyUIDAndIDSubstituted(t *testing.T) {
	var output bytes.Buffer
	r := gin.New()
	r.Use(requestLoggerTo(&output), requestContentLoggerTo(&output))
	for _, name := range []string{"key", "token", "name", "path"} {
		route := "/private/" + name + "/:" + name
		r.GET(route, func(c *gin.Context) { c.Status(200) })
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/private/"+name+"/123456", nil))
		if !strings.Contains(output.String(), route) {
			t.Errorf("missing template %s", route)
		}
	}
	if strings.Contains(output.String(), "123456") {
		t.Fatalf("other parameter exposed: %s", output.String())
	}
	for _, p := range []string{"/Users/1/Unknown/42", "/Items/42/Unknown", "/emby/Items/42/Unknown"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if strings.Contains(output.String(), "/42") {
		t.Fatalf("unknown endpoint must remain masked: %s", output.String())
	}
}
