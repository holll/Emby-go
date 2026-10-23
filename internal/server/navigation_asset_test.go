package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNavigationEmbeddedAsset(t *testing.T) {
	a, _, _ := authorityApp(t)
	h := a.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/web/navigation.js", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") == "" || w.Body.Len() == 0 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	req := httptest.NewRequest("GET", "/web/navigation.js", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/admin", nil))
	nav := strings.Index(w.Body.String(), `src="/web/navigation.js"`)
	app := strings.Index(w.Body.String(), `src="/web/app.js"`)
	if w.Code != 200 || nav < 0 || app < nav {
		t.Fatal(w.Code, "missing navigation script or wrong order")
	}
}
