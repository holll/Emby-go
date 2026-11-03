package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"emby-go/internal/store"
)

func TestPreviewManualProviderRestrictionAndStaleNumber(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"provider":"Other","id":"other","number":"NEW-002"},
			{"provider":"fanza","id":"stale","number":"OLD-001"},
			{"provider":"FANZA","id":"requested","number":"NEW-002"}
		]}`))
	}))
	defer ts.Close()
	s := New(Config{BaseURL: ts.URL, Token: "test"}, nil)
	m := store.Movie{ID: 1, Number: "OLD-001"}
	result, err := s.PreviewManual(context.Background(), m, ManualSearch{Query: manualString("NEW-002"), MetaTubeID: manualString("FANZA")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 || result.Recommended != 1 || !result.Exact || result.Candidates[0].Exact || !result.Candidates[1].Exact {
		t.Fatalf("restriction or manual number matching failed: %+v", result)
	}
	result, err = s.PreviewManual(context.Background(), m, ManualSearch{Query: manualString("https://example.com/movie/id"), MetaTubeID: manualString("NotPresent")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || result.Recommended != -1 || result.Exact || result.Expected != "" {
		t.Fatalf("URL resolution must not escape provider restriction: %+v", result)
	}
}

func TestPreviewManualCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, opts := range []ManualSearch{
		{Query: manualString("NEW-002")},
		{MetaTubeID: manualString("FANZA:opaque-id")},
	} {
		s := New(Config{BaseURL: "http://127.0.0.1:1", Token: "test"}, nil)
		if _, err := s.PreviewManual(ctx, store.Movie{}, opts); err == nil {
			t.Fatal("cancelled request succeeded")
		}
	}
}
