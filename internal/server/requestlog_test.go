package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactedRequestQuery(t *testing.T) {
	for _, query := range []string{
		"api_key=secret", "ApiKey=secret", "X-Emby-Token=secret",
		"X-MediaBrowser-Token=secret", "TOKEN=secret", "access_token=secret",
		"api%5Fkey=secret", "api_key=secret&api_key=another-secret",
		"api_key=secret%2Bvalue%26tail", "api_key=secret&bad=%ZZ",
		"api_key=secret;other=value",
	} {
		t.Run(query, func(t *testing.T) {
			got := redactedRequestQuery(query)
			if strings.Contains(got, "secret") || !strings.Contains(got, "REDACTED") {
				t.Fatalf("凭据未脱敏：%s", got)
			}
		})
	}
	for _, query := range []string{"", "Limit=10"} {
		if got := redactedRequestQuery(query); got != query {
			t.Errorf("允许的参数被修改：%q，期望 %q", got, query)
		}
	}
	got := redactedRequestQuery("api_key=secret&Static=true&MediaSourceId=1")
	values, err := url.ParseQuery(got)
	if err != nil || values.Get("Static") != "true" || values.Get("MediaSourceId") != "1" {
		t.Fatalf("非敏感参数未保留：%s，错误：%v", got, err)
	}
}

func TestRequestLogRedactsTokenWithoutChangingRequest(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var output bytes.Buffer
			router := gin.New()
			router.Use(gin.LoggerWithConfig(gin.LoggerConfig{
				Output: &output, Skip: skipRequestLog, Formatter: requestLogFormatter,
			}))
			const target = "/Videos/1/stream?api_key=secret%2Bvalue%26tail&Static=true"
			router.GET("/Videos/1/stream", func(c *gin.Context) {
				if got := requestToken(c); got != "secret+value&tail" {
					t.Errorf("鉴权凭据被修改：%q", got)
				}
				if c.Request.RequestURI != target || c.Request.URL.RawQuery != strings.SplitN(target, "?", 2)[1] {
					t.Error("原始请求被修改")
				}
				c.Status(status)
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			logged := output.String()
			if strings.Contains(logged, "secret") || !strings.Contains(logged, "REDACTED") || !strings.Contains(logged, "Static=true") {
				t.Fatalf("日志脱敏结果错误：%s", logged)
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
		{"/emby/videos/abc/proxy", true},
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
