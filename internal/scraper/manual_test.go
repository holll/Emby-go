package scraper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"emby-go/internal/store"
)

func manualString(s string) *string { return &s }

func TestManualSearchValidation(t *testing.T) {
	cases := []ManualSearch{
		{}, {Query: manualString("")}, {Query: manualString(" \t ")},
		{MetaTubeID: manualString("FANZA")}, {MetaTubeID: manualString("")},
		{MetaTubeID: manualString(":id")}, {MetaTubeID: manualString("FANZA:")},
		{MetaTubeID: manualString("FANZA:%zz")}, {MetaTubeID: manualString("bad provider:id")},
		{MetaTubeID: manualString("FANZA:a%2Fb")}, {MetaTubeID: manualString("FANZA:%2E%2E")},
		{MetaTubeID: manualString("FANZA:a%00b")}, {MetaTubeID: manualString("FANZA:a%20b")},
		{MetaTubeID: manualString("FANZA:a%3Ab")}, {MetaTubeID: manualString("FANZA:a%5Cb")},
		{MetaTubeID: manualString("FANZA:a/b")}, {MetaTubeID: manualString("FANZA:..")},
		{MetaTubeID: manualString("FANZA:a b")}, {MetaTubeID: manualString("FANZA:id"), Query: manualString("")},
		{Query: manualString("http://")}, {Query: manualString("https:/missing-host")},
		{Query: manualString("ftp://example.com/movie")}, {Query: manualString("https://user:password@example.com/movie")},
		{Query: manualString("hello\nworld")},
	}
	for _, opts := range cases {
		if err := opts.Validate(); !errors.Is(err, ErrInvalidManualSearch) {
			t.Errorf("Validate(%+v) = %v", opts, err)
		}
		if _, err := (*Scraper)(nil).PreviewManual(context.Background(), store.Movie{}, opts); !errors.Is(err, ErrInvalidManualSearch) {
			t.Errorf("PreviewManual must reject before accessing the client: %v", err)
		}
	}
}

func TestPreviewManualRequests(t *testing.T) {
	const rawURL = "https://www.example.com/movie/opaque-id?x=one%2Ftwo&name=a+b#details"
	cases := []struct {
		name            string
		opts            ManualSearch
		path            string
		params          url.Values
		query, expected string
	}{
		{"query", ManualSearch{Query: manualString(" NEW-002 ")}, "/v1/movies/search", url.Values{"q": {"NEW-002"}, "provider": {""}, "fallback": {"true"}}, "NEW-002", "NEW-002"},
		{"provider", ManualSearch{Query: manualString("NEW-002"), MetaTubeID: manualString("FANZA")}, "/v1/movies/search", url.Values{"q": {"NEW-002"}, "provider": {"FANZA"}, "fallback": {"true"}}, "NEW-002", "NEW-002"},
		{"opaque ID", ManualSearch{MetaTubeID: manualString("FANZA:abc00123")}, "/v1/movies/FANZA/abc00123", url.Values{"lazy": {"true"}}, "FANZA:abc00123", ""},
		{"encoded plugin ID", ManualSearch{MetaTubeID: manualString("FANZA:abc%300123")}, "/v1/movies/FANZA/abc00123", url.Values{"lazy": {"true"}}, "FANZA:abc00123", ""},
		{"plugin suffix", ManualSearch{MetaTubeID: manualString("FANZA:abc00123:0")}, "/v1/movies/FANZA/abc00123", url.Values{"lazy": {"true"}}, "FANZA:abc00123", ""},
		{"ID overrides query", ManualSearch{MetaTubeID: manualString("FANZA:abc00123"), Query: manualString("NEW-002")}, "/v1/movies/FANZA/abc00123", url.Values{"lazy": {"true"}}, "FANZA:abc00123", ""},
		{"URL intact", ManualSearch{Query: manualString(rawURL)}, "/v1/movies/search", url.Values{"q": {rawURL}, "provider": {""}, "fallback": {"true"}}, rawURL, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != tc.path || !reflect.DeepEqual(r.URL.Query(), tc.params) {
					t.Errorf("request = %s %v; want %s %v", r.URL.Path, r.URL.Query(), tc.path, tc.params)
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/movies/search" {
					_, _ = w.Write([]byte(`{"data":[{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc00123","number":"NEW-002","title":"new title"}}`))
				}
			}))
			defer ts.Close()
			s := New(Config{BaseURL: ts.URL, Token: "test"}, nil)
			got, err := s.PreviewManual(context.Background(), store.Movie{ID: 42, Number: "OLD-001", SourcePath: "/old/OLD-001.strm"}, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.MovieID != 42 || got.Query != tc.query || got.Expected != tc.expected || got.Recommended != 0 || len(got.Candidates) != 1 {
				t.Fatalf("calls=%d result=%+v", calls, got)
			}
			if got.Candidates[0].ID != "abc00123" || got.Candidates[0].Number != "NEW-002" || got.Exact != (tc.expected != "") {
				t.Fatalf("identity or exact matching incorrect: %+v", got)
			}
		})
	}
}

func TestPreviewManualEmptyAndError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusInternalServerError} {
		for _, direct := range []bool{false, true} {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream failure"}}`))
			}))
			opts := ManualSearch{Query: manualString("NEW-002")}
			if direct {
				opts = ManualSearch{MetaTubeID: manualString("FANZA:opaque-id")}
			}
			got, err := New(Config{BaseURL: ts.URL, Token: "test"}, nil).PreviewManual(context.Background(), store.Movie{}, opts)
			ts.Close()
			if status == http.StatusNotFound {
				if err != nil || len(got.Candidates) != 0 || got.Recommended != -1 {
					t.Fatalf("404: %+v, %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("status %d should fail", status)
			}
		}
	}
}
