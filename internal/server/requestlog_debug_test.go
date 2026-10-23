package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRawDebugCloseForwarding(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		original := &trackedDebugBody{Reader: strings.NewReader("original"), closeErr: errors.New("close failure")}
		if failRead {
			original.Reader = io.MultiReader(original.Reader, debugBodyError{errors.New("read failure")})
		}
		r := gin.New()
		r.Use(requestDiagnostics(true, io.Discard, io.Discard)...)
		r.POST("/body", func(c *gin.Context) {
			data, err := io.ReadAll(c.Request.Body)
			if string(data) != "original" || (err != nil) != failRead {
				t.Errorf("data=%q err=%v", data, err)
			}
			if err := c.Request.Body.Close(); err != original.closeErr {
				t.Errorf("close error=%v", err)
			}
			c.Status(204)
		})
		req := httptest.NewRequest("POST", "/body", nil)
		req.Body, req.ContentLength = original, -1
		r.ServeHTTP(httptest.NewRecorder(), req)
		if original.closes != 1 {
			t.Errorf("closes=%d", original.closes)
		}
	}
}

type trackedDebugBody struct {
	io.Reader
	closes   int
	closeErr error
}

func (r *trackedDebugBody) Close() error { r.closes++; return r.closeErr }

func TestRawDebugBrokenConnectionAndPublicError(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, errno := range []syscall.Errno{syscall.EPIPE, syscall.ECONNRESET} {
			var logs bytes.Buffer
			r := gin.New()
			r.Use(requestDiagnostics(raw, &logs, io.Discard)...)
			r.GET("/broken", func(c *gin.Context) {
				panic(&net.OpError{Op: "BROKEN_SECRET", Err: &os.SyscallError{Syscall: "write", Err: errno}})
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/broken", nil))
			if w.Code == 500 || strings.Contains(logs.String(), "BROKEN_SECRET") != raw {
				t.Fatalf("raw=%v status=%d logs=%s", raw, w.Code, logs.String())
			}
		}
		var logs bytes.Buffer
		r := gin.New()
		r.Use(requestDiagnostics(raw, &logs, io.Discard)...)
		r.GET("/public", func(c *gin.Context) { c.Error(errors.New("PUBLIC_SECRET")).SetType(gin.ErrorTypePublic); c.Status(400) })
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/public", nil))
		if strings.Contains(logs.String(), "PUBLIC_SECRET") != raw {
			t.Fatalf("raw=%v logs=%s", raw, logs.String())
		}
	}
}

func TestRawDebugBinaryUploadAndLargerBudget(t *testing.T) {
	var logs bytes.Buffer
	payload := []byte{0, 255, 10, 13, 'a', '"', '\\'}
	r := gin.New()
	r.Use(requestDiagnostics(true, &logs, io.Discard)...)
	path := "/api/admin/items/1/images/primary"
	r.POST(path, func(c *gin.Context) {
		data, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(400)
			return
		}
		c.Data(200, "application/octet-stream", data)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(payload)))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) || !strings.Contains(logs.String(), fmt.Sprintf("body=%q", payload)) {
		t.Fatal("binary replay or lossless escaped log changed")
	}
	// Exceeds the ordinary 1 MiB budget but remains a permitted image upload.
	large := strings.Repeat("x", int(defaultRequestBodyLimit)+1)
	r = gin.New()
	r.Use(requestDiagnostics(true, io.Discard, io.Discard)...)
	r.POST(path, func(c *gin.Context) {
		n, err := io.Copy(io.Discard, c.Request.Body)
		if err != nil || n != int64(len(large)) {
			t.Errorf("n=%d err=%v", n, err)
		}
		c.Status(204)
	})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(large)))
	if w.Code != 204 {
		t.Fatalf("image allowance status=%d", w.Code)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader("not read"))
	req.ContentLength = imageUploadBodyLimit + 1
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 413 {
		t.Fatalf("upload overlimit status=%d", w.Code)
	}
}

func TestRawDebugAppConfigurationIsolation(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "requests-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	// Server tests are serial and do not initialize the process-wide logging
	// singleton, so newApp uses the captured RequestWriter stdout fallback.
	for _, raw := range []bool{false, true, false} {
		start, _ := file.Seek(0, io.SeekCurrent)
		a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "debug.db"), Debug: raw}, cache.NewMemory(16))
		if err != nil {
			t.Fatal(err)
		}
		a.startupWG.Wait()
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/health?Unknown=INSTANCE_SECRET", nil))
		a.Close()
		if w.Code != 200 {
			t.Fatalf("status=%d", w.Code)
		}
		end, _ := file.Seek(0, io.SeekCurrent)
		data := make([]byte, end-start)
		if _, err := file.ReadAt(data, start); err != nil {
			t.Fatal(err)
		}
		// 查询串在 raw debug 中也被遮蔽；debug 不是绕过凭据边界的理由。
		if strings.Contains(string(data), "INSTANCE_SECRET") {
			t.Fatalf("raw=%v logs leaked query secret: %s", raw, data)
		}
		if raw && !strings.Contains(string(data), "redacted") {
			t.Fatalf("raw debug missing redacted query marker: %s", data)
		}
		if gin.Mode() != gin.ReleaseMode {
			t.Fatal("global Gin mode changed")
		}
	}
}

func TestRawDebugRequestLogs(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(map[bool]string{false: "safe_default", true: "raw_debug"}[raw], func(t *testing.T) {
			var logs, app bytes.Buffer
			r := gin.New()
			r.Use(requestDiagnostics(raw, &logs, &app)...)
			body := `{"Password":"BODY_SECRET","unknown":"original value"}`
			r.POST("/emby/Users/:uid/Items/:id", func(c *gin.Context) {
				got, err := io.ReadAll(c.Request.Body)
				if err != nil || string(got) != body || c.Request.Header.Get("Authorization") != "Emby Token=AUTH_SECRET" {
					t.Errorf("request changed: body=%q err=%v", got, err)
				}
				_ = c.Error(errors.New("HANDLER_SECRET"))
				c.Status(http.StatusBadRequest)
			})
			uri := "/emby/Users/1/Items/42?IncludeItemTypes=BoxSet&ListItemIds=42&api_key=QUERY_SECRET&Unknown=UNKNOWN_SECRET"
			req := httptest.NewRequest(http.MethodPost, uri, strings.NewReader(body))
			req.Header.Set("Authorization", "Emby Token=AUTH_SECRET")
			req.Header.Set("Cookie", "session=COOKIE_SECRET")
			req.Header.Set("X-Unknown", "HEADER_SECRET")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d", w.Code)
			}
			// 非敏感路由：body/普通 header/gin error 仍可原样诊断；
			// 认证头、Cookie 与全部查询串永远不得进入原始日志。
			for _, visibility := range []struct {
				secret  string
				present bool
			}{
				{"BODY_SECRET", raw}, {"HEADER_SECRET", raw}, {"HANDLER_SECRET", raw},
				{"AUTH_SECRET", false}, {"COOKIE_SECRET", false},
				{"QUERY_SECRET", false}, {"UNKNOWN_SECRET", false},
			} {
				if got := strings.Contains(logs.String(), visibility.secret); got != visibility.present {
					t.Errorf("raw=%v secret %s presence=%v want=%v in %s", raw, visibility.secret, got, visibility.present, logs.String())
				}
			}
			if raw && !strings.Contains(logs.String(), "/emby/Users/1/Items/42?redacted") {
				t.Fatalf("raw query marker lost: %s", logs.String())
			}
			if !raw && strings.Contains(logs.String(), "ListItemIds") {
				t.Fatal("safe unknown-query policy changed")
			}
			if app.Len() != 0 {
				t.Fatalf("raw request leaked into app log: %s", app.String())
			}
		})
	}
}

func TestRawDebugMasksCredentialRoutes(t *testing.T) {
	for _, raw := range []bool{false, true} {
		var logs bytes.Buffer
		r := gin.New()
		r.Use(requestDiagnostics(raw, &logs, io.Discard)...)
		r.POST("/Users/AuthenticateByName", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
		r.DELETE("/api/admin/apikeys/:key", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		body := `{"Username":"admin","Pw":"AUTH_BODY_SECRET"}`
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName?api_key=QUERY_SECRET", strings.NewReader(body))
		req.Header.Set("Authorization", "Emby Token=AUTH_HEADER_SECRET")
		req.Header.Set("X-Emby-Token", "EMBY_TOKEN_SECRET")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", w.Code)
		}
		before := logs.Len()
		req = httptest.NewRequest(http.MethodDelete, "/api/admin/apikeys/APIKEY_PATH_SECRET", nil)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		combined := logs.String()
		for _, secret := range []string{"AUTH_BODY_SECRET", "QUERY_SECRET", "AUTH_HEADER_SECRET", "EMBY_TOKEN_SECRET", "APIKEY_PATH_SECRET"} {
			if strings.Contains(combined, secret) {
				t.Fatalf("raw=%v sensitive route leaked %s: %s", raw, secret, combined)
			}
		}
		if raw && !strings.Contains(combined, "[sensitive route]") {
			t.Fatalf("raw debug missing sensitive marker: %s", combined)
		}
		if got := logs.String()[before:]; strings.Contains(got, "redacted sensitive route") == raw == false {
			_ = got
		}
	}
}

func TestRawDebugIncludesQuietAndUnmatchedRequests(t *testing.T) {
	for _, raw := range []bool{false, true} {
		var logs bytes.Buffer
		r := gin.New()
		r.Use(requestDiagnostics(raw, &logs, io.Discard)...)
		for _, path := range []string{"/web/app.js", "/Items/1/Images/Primary", "/Videos/1/stream"} {
			r.GET(path, func(c *gin.Context) { c.Status(http.StatusOK) })
		}
		for _, path := range []string{"/web/app.js", "/Items/1/Images/Primary", "/Videos/1/stream", "/unknown/PATH_SECRET"} {
			before := logs.Len()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			part := logs.String()[before:]
			if raw && !strings.Contains(part, path) {
				t.Errorf("raw missing %s: %s", path, part)
			}
			if !raw && path != "/unknown/PATH_SECRET" && part != "" {
				t.Errorf("safe quiet path logged: %s", part)
			}
			if !raw && strings.Contains(part, "PATH_SECRET") {
				t.Fatal("safe unknown path exposed")
			}
		}
	}
}

func TestRawDebugPanicAndUnreadBody(t *testing.T) {
	var logs bytes.Buffer
	r := gin.New()
	r.Use(requestDiagnostics(true, &logs, io.Discard)...)
	r.POST("/panic", func(c *gin.Context) { panic("PANIC_SECRET") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/panic", strings.NewReader("UNREAD_BODY_SECRET")))
	if w.Code != 500 || !strings.Contains(logs.String(), "PANIC_SECRET") || !strings.Contains(logs.String(), "UNREAD_BODY_SECRET") || !strings.Contains(logs.String(), "goroutine") {
		t.Fatalf("status=%d log=%s", w.Code, logs.String())
	}
}

func TestRawDebugPreservesBodyReadErrorAndLimit(t *testing.T) {
	readFailure := errors.New("READ_FAILURE")
	for _, raw := range []bool{false, true} {
		var logs bytes.Buffer
		r := gin.New()
		r.Use(requestDiagnostics(raw, &logs, io.Discard)...)
		r.POST("/body", func(c *gin.Context) {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.String(400, "%s|%s", body, err)
				return
			}
			c.String(200, "%s", body)
		})
		request := httptest.NewRequest(http.MethodPost, "/body", nil)
		request.Body = io.NopCloser(io.MultiReader(strings.NewReader("partial"), debugBodyError{readFailure}))
		request.ContentLength = -1
		w := httptest.NewRecorder()
		r.ServeHTTP(w, request)
		if w.Code != 400 || w.Body.String() != "partial|READ_FAILURE" {
			t.Fatalf("raw=%v status=%d body=%s", raw, w.Code, w.Body)
		}
		for _, chunked := range []bool{false, true} {
			request = httptest.NewRequest(http.MethodPost, "/body", strings.NewReader(strings.Repeat("x", int(defaultRequestBodyLimit)+1)))
			if chunked {
				request.ContentLength = -1
			}
			w = httptest.NewRecorder()
			r.ServeHTTP(w, request)
			want := 413
			if chunked {
				want = 400
			}
			if w.Code != want {
				t.Errorf("raw=%v chunked=%v status=%d want=%d", raw, chunked, w.Code, want)
			}
		}
	}
}
