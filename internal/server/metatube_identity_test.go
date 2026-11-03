package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"emby-go/internal/scraper"
	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

func TestScrapePreviewStoredIdentityAPI(t *testing.T) {
	app, ts, token := contractEnv(t)
	requests := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/movies/search" {
			_, _ = w.Write([]byte(`{"data":[{"provider":"Other","id":"manual","number":"NEW-002","title":"Manual"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"provider":"FANZA","id":"abc+123","number":"DIFFERENT-001","title":"Stored"}}`))
		}
	}))
	defer upstream.Close()
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "test-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	m, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	m.ProviderID = "FANZA:abc%2B123:0.5:True"
	if _, err = app.db.UpsertMovie(m, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, path, id string }{
		{"", "/v1/movies/FANZA/abc+123", "abc+123"},
		{"?q=NEW-002", "/v1/movies/search", "manual"},
		{"?metatube_id=FANZA%3Aabc%252B123%3A%3ATrue", "/v1/movies/FANZA/abc+123", "abc+123"},
	} {
		resp, raw := embyRaw(t, ts, "GET", "/api/admin/items/1/scrape/preview"+tc.query, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		var got scraper.PreviewResult
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Candidates) != 1 || got.Candidates[0].ID != tc.id {
			t.Fatalf("preview: %+v", got)
		}
		if path := <-requests; path != tc.path {
			t.Fatalf("upstream %s, want %s", path, tc.path)
		}
	}
	after, err := os.ReadFile(m.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preview modified NFO")
	}
	indexed, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.ProviderID != m.ProviderID {
		t.Fatalf("preview modified stored identity: %q", indexed.ProviderID)
	}
}

func TestMetaTubeIdentityDTO(t *testing.T) {
	for _, tc := range []struct{ raw, official string }{
		{"FANZA:abc00123", "FANZA:abc00123"},
		{"FANZA:abc%2B123:0.7:True", "FANZA:abc%2B123:0.7:True"},
		{"metatube:FANZA:abc+123", "FANZA:abc%2B123"},
		{"FANZA:abc::True", "FANZA:abc::True"},
		{"metatube:../bad:id", ""},
		{"invalid", ""},
		{"", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			dto := (&App{}).embyItemActors(store.Movie{ID: 1, ProviderID: tc.raw}, store.UserData{}, nil, true)
			ids, _ := dto["ProviderIds"].(gin.H)
			if tc.raw == "" {
				if ids != nil {
					t.Fatalf("unexpected empty IDs: %v", ids)
				}
				return
			}
			if ids["metatube"] != tc.raw {
				t.Fatalf("legacy key changed: %v", ids)
			}
			if tc.official == "" {
				if _, ok := ids["MetaTube"]; ok {
					t.Fatalf("invalid identity exported: %v", ids)
				}
			} else if ids["MetaTube"] != tc.official {
				t.Fatalf("official key = %v, want %s", ids, tc.official)
			}
		})
	}
}
