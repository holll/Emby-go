package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardEmbeddedAssets(t *testing.T) {
	a, _, _ := authorityApp(t)
	h := a.Handler()
	for _, route := range []string{"/web/dashboard.js", "/web/dashboard.css"} {
		t.Run(route, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
			want := "javascript"
			if strings.HasSuffix(route, ".css") {
				want = "text/css"
			}
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), want) || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") == "" || w.Body.Len() == 0 {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
			req := httptest.NewRequest("GET", route, nil)
			req.Header.Set("If-None-Match", w.Header().Get("ETag"))
			cached := httptest.NewRecorder()
			h.ServeHTTP(cached, req)
			if cached.Code != 304 || cached.Body.Len() != 0 {
				t.Fatal(cached.Code, cached.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/admin", nil))
	body := w.Body.String()
	dashboard, app := strings.Index(body, `src="/web/dashboard.js"`), strings.Index(body, `src="/web/app.js"`)
	if w.Code != 200 || dashboard < 0 || app < dashboard || !strings.Contains(body, `href="/web/dashboard.css"`) {
		t.Fatal("missing or incorrectly ordered dashboard assets", w.Code)
	}
}
