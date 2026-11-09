package server

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDiagnosticRouteActualIDs(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	for _, tc := range []struct{ uid, id string }{
		{"1", "42"}, {"23", "1000000000001"}, {"1", "9223372036854775807"},
		{"1", "part-42-2"}, {"1", "boxsets"}, {"1", "boxset:U2VyaWVzIEE"},
		{"canary-user", "42"}, {"1", "canary-path"}, {"1", "9223372036854775808"},
		{"1", "part-canary-2"}, {"1", "boxset:canary="},
		{"1", "boxset:" + strings.Repeat("A", 516)}, {"1", "canary\npath"},
	} {
		t.Run(tc.uid+"/"+tc.id, func(t *testing.T) {
			for _, prefix := range []string{"", "/emby"} {
				var output bytes.Buffer
				r := gin.New()
				r.Use(requestLoggerTo(&output))
				target := prefix + "/Users/" + url.PathEscape(tc.uid) + "/Items/" + url.PathEscape(tc.id) + "?token=canary-query&UserId=1&ParentId=1000000000001"
				r.GET(prefix+"/Users/:uid/Items/:id", func(c *gin.Context) {
					if c.Param("uid") != tc.uid || c.Param("id") != tc.id || c.Request.RequestURI != target {
						t.Error("logging changed request")
					}
					c.Status(200)
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
				if got := output.String(); w.Code != 200 || strings.Count(got, fmt.Sprintf("%q", target)) != 1 {
					t.Fatalf("status=%d want=%q log=%s", w.Code, target, got)
				}
			}
		})
	}
}

func TestDiagnosticEncodedIDsRemainEscaped(t *testing.T) {
	for _, raw := range []string{"%34%32", "42%250Acanary", "42%0Acanary", "42%2Fcanary"} {
		var output bytes.Buffer
		r := gin.New()
		r.Use(requestLoggerTo(&output))
		r.GET("/Items/:id", func(c *gin.Context) { c.Status(200) })
		target := "/Items/" + raw
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
		if got := output.String(); !strings.Contains(got, fmt.Sprintf("%q", target)) {
			t.Fatalf("escaped ID changed: %s", got)
		}
	}
}

func TestDiagnosticActualIDsOnFailure(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		var output bytes.Buffer
		r := gin.New()
		r.Use(requestLoggerTo(&output), requestRecoveryTo(&output))
		r.GET("/emby/Users/:uid/Items/:id", func(c *gin.Context) {
			if status == 500 {
				panic("canary-panic")
			}
			c.AbortWithStatus(status)
		})
		w := httptest.NewRecorder()
		const target = "/emby/Users/1/Items/42?%61pi_key=canary-one&api_key=canary-two&Token=canary-three"
		r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if got := output.String(); w.Code != status || strings.Count(got, fmt.Sprintf("%q", target)) != 1 || strings.Contains(got, "canary-panic") {
			t.Fatalf("status=%d log=%s", w.Code, got)
		}
	}
}

func TestDiagnosticAllPathParametersAndUnknownRoutesPreserved(t *testing.T) {
	var output bytes.Buffer
	r := gin.New()
	r.Use(requestLoggerTo(&output))
	for _, name := range []string{"key", "token", "name", "path"} {
		r.GET("/private/"+name+"/:"+name, func(c *gin.Context) { c.Status(200) })
		target := "/private/" + name + "/123456"
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
		if !strings.Contains(output.String(), fmt.Sprintf("%q", target)) {
			t.Errorf("missing original path %s", target)
		}
	}
	for _, target := range []string{"/Users/1/Unknown/42", "/Items/42/Unknown", "/emby/Items/42/Unknown"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
		if !strings.Contains(output.String(), fmt.Sprintf("%q", target)) {
			t.Errorf("unknown path lost: %s", target)
		}
	}
}
