package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestLogPreservesRequestURI(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, target := range []string{
			"/unknown/%2f%41%0a?z=last&api_key=secret%2Bvalue%26tail&token=one&token=two&a=first",
			"/unknown/path?api%5Fkey=secret&bad=%ZZ",
			"/unknown/path?api_key=secret;other=value",
			"/unknown/path?",
			"/unknown/path",
			"/Users/AuthenticateByName?token=credential-query",
			"/api/admin/apikeys/credential-path?api_key=credential-query",
		} {
			t.Run(fmt.Sprintf("raw=%t/%s", raw, target), func(t *testing.T) {
				var output bytes.Buffer
				router := gin.New()
				if raw {
					router.Use(rawRequestLoggerTo(&output))
				} else {
					router.Use(requestLoggerTo(&output))
				}
				router.NoRoute(func(c *gin.Context) {
					if c.Request.RequestURI != target || c.Request.URL.RequestURI() != target {
						t.Errorf("logging changed request: %+v", c.Request.URL)
					}
					c.Status(http.StatusNotFound)
				})
				req := httptest.NewRequest(http.MethodGet, target, nil)
				router.ServeHTTP(httptest.NewRecorder(), req)
				if logged := output.String(); !strings.Contains(logged, fmt.Sprintf("%q", target)) {
					t.Fatalf("request URI changed: want=%q log=%s", target, logged)
				}
			})
		}
	}
}

func TestRequestLogRequestURIFallback(t *testing.T) {
	for _, raw := range []bool{false, true} {
		var output bytes.Buffer
		router := gin.New()
		if raw {
			router.Use(rawRequestLoggerTo(&output))
		} else {
			router.Use(requestLoggerTo(&output))
		}
		for _, target := range []string{"/fallback/%2f?token=a&api_key=b&z=1", "/fallback/path?"} {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.RequestURI = ""
			router.ServeHTTP(httptest.NewRecorder(), req)
			if !strings.Contains(output.String(), fmt.Sprintf("%q", target)) {
				t.Fatalf("raw=%t fallback changed %q: %s", raw, target, output.String())
			}
		}
	}
}

func TestRequestLogRetainsTokenWithoutChangingRequest(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var output bytes.Buffer
			router := gin.New()
			router.Use(requestLoggerTo(&output))
			const target = "/Videos/1/stream?api_key=secret%2Bvalue%26tail&Static=true"
			router.GET("/Videos/1/stream", func(c *gin.Context) {
				if got := requestToken(c); got != "secret+value&tail" {
					t.Errorf("credential changed: %q", got)
				}
				if c.Request.RequestURI != target || c.Request.URL.RawQuery != strings.SplitN(target, "?", 2)[1] {
					t.Error("request changed")
				}
				c.Status(status)
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			if logged := output.String(); !strings.Contains(logged, fmt.Sprintf("%q", target)) {
				t.Fatalf("request URI lost: %s", logged)
			}
		})
	}
}

func TestQuietPath(t *testing.T) {
	cases := []struct {
		path  string
		quiet bool
	}{
		{"/web/app.js", true},
		{"/web/vendor/artplayer.min.js", true},
		{"/favicon.ico", true},
		{"/Items/abc/Images/Primary", true},
		{"/emby/Items/abc/Images/Primary/0", true},
		{"/Videos/abc/stream", true},
		{"/Videos/abc/stream.mkv", true},
		{"/audio/abc/stream", true},

		// 需要保留记录的接口
		{"/System/Info/Public", false},
		{"/Users/AuthenticateByName", false},
		{"/api/admin/items", false},
		{"/", false},
		{"/Items/abc/PlaybackInfo", false},
		{"/Users/uid/Items", false},
		{"/web", false}, // 管理端首页本体，非静态资源
	}
	for _, tc := range cases {
		if got := quietPath(tc.path); got != tc.quiet {
			t.Errorf("quietPath(%q) = %v，期望 %v", tc.path, got, tc.quiet)
		}
	}
}
