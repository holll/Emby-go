package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"emby-go/internal/scraper"
)

func TestScrapePreviewManualAPI(t *testing.T) {
	app, ts, token := contractEnv(t)
	var calls atomic.Int32
	requests := make(chan *http.Request, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		requests <- r
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("q") == "upstream-error" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
			return
		}
		if r.URL.Path == "/v1/movies/search" {
			_, _ = w.Write([]byte(`{"data":[{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}}`))
		}
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	before, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	nfoBefore, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	const rawURL = "https://example.com/movie/abc00123?x=one%2Ftwo&name=a+b#details"
	cases := []struct {
		name, query, path, upstreamQuery, provider string
	}{
		{"automatic unchanged", "", "/v1/movies/search", "ABF-018", ""},
		{"manual query", "q=NEW-002", "/v1/movies/search", "NEW-002", ""},
		{"provider query", "q=NEW-002&metatube_id=FANZA", "/v1/movies/search", "NEW-002", "FANZA"},
		{"precise ID", "metatube_id=FANZA%3Aabc00123", "/v1/movies/FANZA/abc00123", "", ""},
		{"precise URL", "q=" + url.QueryEscape(rawURL), "/v1/movies/search", rawURL, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := embyRaw(t, ts, "GET", "/api/admin/items/1/scrape/preview?"+tc.query, token, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, raw)
			}
			var got scraper.PreviewResult
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.MovieID != 1 || len(got.Candidates) != 1 || got.Candidates[0].ID != "abc00123" {
				t.Fatalf("preview=%+v", got)
			}
			r := <-requests
			if r.URL.Path != tc.path || r.URL.Query().Get("q") != tc.upstreamQuery || r.URL.Query().Get("provider") != tc.provider {
				t.Fatalf("upstream request %s", r.URL.String())
			}
		})
	}
	for _, query := range []string{
		"q=", "q=%20%20", "metatube_id=FANZA", "metatube_id=", "q=NEW-002&metatube_id=",
		"metatube_id=%3Aid", "metatube_id=FANZA%3A", "metatube_id=FANZA%3Aid%3Aextra",
		"metatube_id=FANZA%3Aid&q=", "q=a&q=b", "q=a&metatube_id=FANZA&metatube_id=JAVDB",
		"q=http%3A%2F%2F", "q=ftp%3A%2F%2Fexample.com%2Fmovie", "q=hello%0Aworld", "q=%zz", "q=a;b",
	} {
		t.Run("reject "+query, func(t *testing.T) {
			count := calls.Load()
			resp, raw := embyRaw(t, ts, "GET", "/api/admin/items/1/scrape/preview?"+query, token, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d: %s", resp.StatusCode, raw)
			}
			if calls.Load() != count {
				t.Fatal("invalid input contacted MetaTube")
			}
		})
	}
	resp, raw := embyRaw(t, ts, "GET", "/api/admin/items/1/scrape/preview?q=upstream-error", token, "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("upstream error: %d %s", resp.StatusCode, raw)
	}
	<-requests
	resp, raw = embyRaw(t, ts, "GET", "/api/admin/items/99999/scrape/preview?q=NEW-002", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing item: %d %s", resp.StatusCode, raw)
	}
	after, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed indexed movie: before=%+v after=%+v", before, after)
	}
	nfoAfter, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nfoBefore, nfoAfter) {
		t.Fatal("preview changed NFO")
	}
	if owner := app.nfoBusyOwner(); owner != "" {
		t.Fatalf("preview claimed NFO gate: %s", owner)
	}
}
