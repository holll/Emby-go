package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/logging"
)

func readLogStreamEvent(t *testing.T, reader *bufio.Reader) (string, []byte) {
	t.Helper()
	var event string
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("reading stream event:", err)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" && event != "" {
			return event, data
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = []byte(strings.TrimPrefix(line, "data: "))
		}
	}
}

func TestAdminLogsStreamRawSourcesResumeAndRevocation(t *testing.T) {
	dir := diskLogsFixtureDir(t)
	for _, kind := range []string{"app", "request"} {
		text := "2026-11-12 INFO token=RAW_FIXTURE\n{\"level\":\"INFO\"}\n  continuation\r\n\n"
		if err := os.WriteFile(filepath.Join(dir, kind+"-2026-11-12.log"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, token := logsTestApp(t)
	a.router.GET("/stream", noStore, a.requireAuth, a.requireAdmin, a.adminLogsStream)
	server := httptest.NewServer(a.router)
	t.Cleanup(server.Close)
	for _, kind := range []string{"app", "request"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream?kind="+kind, nil)
		req.Header.Set("X-Emby-Token", token)
		response, err := server.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || response.Header.Get("X-Accel-Buffering") != "no" {
			response.Body.Close()
			cancel()
			t.Fatalf("bad stream headers/status: %d", response.StatusCode)
		}
		reader := bufio.NewReader(response.Body)
		event, data := readLogStreamEvent(t, reader)
		var page logging.RawLogPage
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		if event != "logs" || page.Kind != kind || page.Cursor == "" || page.Text != "2026-11-12 INFO token=RAW_FIXTURE\n{\"level\":\"INFO\"}\n  continuation\r\n\n" {
			t.Fatal("raw SSE payload changed text")
		}
		cursor := page.Cursor
		response.Body.Close()
		cancel()
		f, err := os.OpenFile(filepath.Join(dir, kind+"-2026-11-12.log"), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("resumed\n")
		f.Close()
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream?kind="+kind+"&after="+cursor, nil)
		req.Header.Set("X-Emby-Token", token)
		response, err = server.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		reader = bufio.NewReader(response.Body)
		event, data = readLogStreamEvent(t, reader)
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		if event != "logs" || page.Text != "resumed\n" || page.Reset {
			t.Fatal("resume replayed history or changed content")
		}
		if kind == "request" {
			if err := a.db.DeleteAccessToken(token); err != nil {
				t.Fatal(err)
			}
			event, data = readLogStreamEvent(t, reader)
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(data, &failure); err != nil {
				t.Fatal(err)
			}
			if event != "error" || failure.Code != "AUTH_INVALID" {
				t.Fatal("revoked stream not stopped")
			}
		}
		response.Body.Close()
		cancel()
	}
}

func TestAdminLogsStreamValidationAndShutdown(t *testing.T) {
	dir := diskLogsFixtureDir(t)
	os.WriteFile(filepath.Join(dir, "app-2026-11-12.log"), []byte("line\n"), 0o600)
	a, token := logsTestApp(t)
	a.router.GET("/stream", noStore, a.requireAuth, a.requireAdmin, a.adminLogsStream)
	for _, tc := range []struct {
		query  string
		auth   bool
		status int
	}{
		{"?kind=app", false, 401},
		{"?kind=../app", true, 400},
		{"?kind=requests", true, 400},
		{"?after=forged", true, 400},
	} {
		req := httptest.NewRequest(http.MethodGet, "/stream"+tc.query, nil)
		if tc.auth {
			req.Header.Set("X-Emby-Token", token)
		}
		w := httptest.NewRecorder()
		a.router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("query=%s status=%d", tc.query, w.Code)
		}
	}
	server := httptest.NewServer(a.router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream", nil)
	req.Header.Set("X-Emby-Token", token)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readLogStreamEvent(t, reader)
	a.BeginShutdown()
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("stream did not shut down cleanly: %v", err)
	}
}
