package scraper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/store"
)

func TestStoredIdentityPreviewAndBatch(t *testing.T) {
	for _, tc := range []struct{ marker, id string }{
		{"FANZA:opaque%2Bid", "opaque+id"},
		{"FANZA:opaque%2Bid:0", "opaque+id"},
		{"metatube:FANZA:raw%2Bid", "raw%2Bid"},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/movies/FANZA/"+tc.id || r.URL.Query().Get("lazy") != "true" {
					t.Errorf("unexpected request: %s", r.URL)
					http.Error(w, "unexpected search", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"data":{"provider":"FANZA","id":%q,"number":"NEW-002","title":"precise film"}}`, tc.id)
			}))
			defer ts.Close()
			dir := t.TempDir()
			m := store.Movie{ID: 42, ProviderID: tc.marker, Number: "OLD-001", OutputDir: dir, SourcePath: filepath.Join(dir, "OLD-001.strm"), NFOPath: filepath.Join(dir, "movie.nfo")}
			s := New(Config{BaseURL: ts.URL, Token: "test"}, nil)
			preview, err := s.Preview(context.Background(), m)
			if err != nil || len(preview.Candidates) != 1 || preview.Recommended != 0 || preview.Candidates[0].ID != tc.id || preview.Exact {
				t.Fatalf("preview=%+v error=%v", preview, err)
			}
			thumb, parseErr := url.Parse(preview.Candidates[0].Thumb)
			if parseErr != nil || thumb.Query().Get("id") != tc.id {
				t.Fatalf("preview thumbnail loses opaque ID: %q, %v", preview.Candidates[0].Thumb, parseErr)
			}
			result := s.scrapeOne(context.Background(), m, RunOptions{Overwrite: true})
			if result.Status != StatusSuccess || result.Number != "NEW-002" || result.Provider != "FANZA" || calls != 2 {
				t.Fatalf("batch=%+v calls=%d", result, calls)
			}
			raw, err := os.ReadFile(m.NFOPath)
			if err != nil || !strings.Contains(string(raw), providerID("FANZA", tc.id)) {
				t.Fatalf("NFO identity missing: %s, %v", raw, err)
			}
		})
	}
}

func TestStoredIdentityMissingAndErrorsNeverSearch(t *testing.T) {
	for _, status := range []int{404, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/movies/FANZA/precise" {
					t.Errorf("unexpected fallback: %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"missing or failed"}}`))
			}))
			defer ts.Close()
			s := New(Config{BaseURL: ts.URL, Token: "test"}, nil)
			m := store.Movie{ProviderID: "FANZA:precise", Number: "OLD-001"}
			preview, err := s.Preview(context.Background(), m)
			if status == 404 {
				if err != nil || len(preview.Candidates) != 0 || preview.Recommended != -1 {
					t.Fatalf("404 preview=%+v err=%v", preview, err)
				}
			} else if err == nil {
				t.Fatal("expected preview error")
			}
			result := s.scrapeOne(context.Background(), m, RunOptions{})
			want := StatusFailed
			if status == 404 {
				want = StatusSkipped
			}
			if result.Status != want || calls != 2 {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
}

func TestMalformedStoredIdentityUsesSearch(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/movies/search" || r.URL.Query().Get("q") != "OLD-001" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer ts.Close()
	s := New(Config{BaseURL: ts.URL, Token: "test"}, nil)
	m := store.Movie{ProviderID: "FANZA:%zz", Number: "OLD-001"}
	if _, err := s.Preview(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got := s.scrapeOne(context.Background(), m, RunOptions{}); got.Status != StatusSkipped {
		t.Fatal(got)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestPreviewManualOverridesStoredIdentity(t *testing.T) {
	for _, opts := range []ManualSearch{
		{Query: manualString("NEW-002")},
		{MetaTubeID: manualString("FANZA:chosen")},
	} {
		calls := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if opts.Query != nil {
				if r.URL.Path != "/v1/movies/search" || r.URL.Query().Get("q") != "NEW-002" {
					t.Errorf("unexpected stored lookup: %s", r.URL)
				}
				_, _ = w.Write([]byte(`{"data":[{"provider":"FANZA","id":"chosen","number":"NEW-002"}]}`))
			} else {
				if r.URL.Path != "/v1/movies/FANZA/chosen" {
					t.Errorf("unexpected stored lookup: %s", r.URL)
				}
				_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"chosen","number":"NEW-002"}}`))
			}
		}))
		got, err := New(Config{BaseURL: ts.URL, Token: "test"}, nil).PreviewManual(context.Background(), store.Movie{ProviderID: "Other:stored", Number: "OLD-001"}, opts)
		ts.Close()
		if err != nil || calls != 1 || len(got.Candidates) != 1 || got.Candidates[0].ID != "chosen" {
			t.Fatalf("preview=%+v err=%v calls=%d", got, err, calls)
		}
	}
}

func TestPluginAndLegacyIdentityParsing(t *testing.T) {
	for _, tc := range []struct{ marker, provider, id string }{
		{"FANZA:abc%2Bdef", "FANZA", "abc+def"},
		{"FANZA:abc%2Bdef:0", "FANZA", "abc+def"},
		{"metatube:FANZA:abc%2Bdef", "FANZA", "abc%2Bdef"},
		{"metatube:FANZA:raw:id", "FANZA", "raw:id"},
	} {
		p, id, ok := SplitProviderID(tc.marker)
		if !ok || p != tc.provider || id != tc.id {
			t.Errorf("SplitProviderID(%q)=(%q,%q,%v)", tc.marker, p, id, ok)
		}
	}
	if got := providerID("FANZA", "abc+def"); got != "FANZA:abc%2Bdef" {
		t.Fatalf("new identity=%q", got)
	}
}
