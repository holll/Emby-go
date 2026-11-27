package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"emby-go/internal/scraper"
)

func TestAdminScrapeStreamSnapshotsAndTerminal(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, nil)
	path := ts.URL + "/api/admin/scrape/progress/stream"
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	unauthorized, err := ts.Client().Get(path)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", unauthorized.StatusCode)
	}
	if err := app.beginScrape("scrape", 2); err != nil {
		t.Fatal(err)
	}
	defer app.endScrape(nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Emby-Token", token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected stream response: %d %v", resp.StatusCode, resp.Header)
	}
	reader := bufio.NewReader(resp.Body)
	read := func() scrapeStatus {
		t.Helper()
		event, raw := readLogStreamEvent(t, reader)
		var status scrapeStatus
		if err := json.Unmarshal(raw, &status); err != nil || event != "progress" {
			t.Fatalf("invalid progress event: %s %s (%v)", event, raw, err)
		}
		return status
	}
	if got := read(); !got.Running || got.Total != 2 || got.Done != 0 {
		t.Fatalf("initial snapshot: %+v", got)
	}
	app.updateScrapeProgress(scraper.Progress{Total: 2, Done: 1, Success: 1})
	if got := read(); !got.Running || got.Done != 1 {
		t.Fatalf("updated snapshot: %+v", got)
	}
	app.endScrape(nil)
	if got := read(); got.Running || got.FinishedAt == "" || got.Done != 1 {
		t.Fatalf("terminal snapshot: %+v", got)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream did not end after terminal snapshot: %v", err)
	}
}
