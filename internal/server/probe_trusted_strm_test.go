package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/probe"
)

// This exercises trusted local HTTP inputs with the actual ffprobe executable.
// Loopback access is intentional under the trusted-STRM policy, not isolation.
func TestTrustedSTRMProbeFailureKeepsPlaybackAndRedactsResponse(t *testing.T) {
	binary := os.Getenv("EMBY_GO_TEST_FFPROBE")
	if binary == "" {
		t.Skip("set EMBY_GO_TEST_FFPROBE to run the real trusted-STRM regression; skip is not evidence")
	}
	binary, err := probe.LookPath(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, partial := range []bool{false, true} {
		name := "all-failed"
		if partial {
			name = "partial-success"
		}
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int64
			media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path == "/invalid" {
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write([]byte("this is not a media container"))
					return
				}
				http.ServeFile(w, r, "testdata/probe-sample.mp4")
			}))
			defer media.Close()
			source, err := url.Parse(media.URL + "/invalid?token=probe-query-canary")
			if err != nil {
				t.Fatal(err)
			}
			source.User = url.UserPassword("probe-user-canary", "probe-password-canary")
			// Require the real executable's error to contain a credential before
			// checking the API boundary, so a quiet stderr cannot vacuously pass.
			_, rawErr := probe.Run(context.Background(), binary, source.String(), 10*time.Second)
			if rawErr == nil || !strings.Contains(rawErr.Error(), "probe-query-canary") {
				t.Fatalf("fixture must produce a URL-bearing ffprobe error, got %v", rawErr)
			}
			app, ts, token := contractEnv(t)
			app.startupWG.Wait()
			app.cfg.FFProbePath = binary
			movie, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, movie.SourcePath, source.String()+"\n")
			if partial {
				part := filepath.Join(filepath.Dir(movie.SourcePath), "part2.strm")
				writeFile(t, part, media.URL+"/valid.mp4\n")
				movie.AdditionalParts = []string{part}
				if _, err := indexFixtureMovie(t, app, movie, 0, time.Now()); err != nil {
					t.Fatal(err)
				}
				movie, err = app.db.Movie(1)
				if err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(movie.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			app.cfg.FFProbePath = filepath.Join(t.TempDir(), "missing-ffprobe")
			resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/probe", token, "")
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("missing ffprobe response=%d %s", resp.StatusCode, body)
			}
			assertTrustedSTRMPlayback(t, ts, token, source.String(), &hits)
			app.cfg.FFProbePath = binary
			logs := captureLogs(t)
			resp, body = embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/probe", token, "")
			want := http.StatusBadGateway
			if partial {
				want = http.StatusOK
			}
			if resp.StatusCode != want || !strings.Contains(string(body), "Invalid data found") {
				t.Fatalf("probe response=%d %s", resp.StatusCode, body)
			}
			if partial {
				var result struct {
					Files    []struct{ Kind string }
					Failures []string
				}
				if err := json.Unmarshal(body, &result); err != nil || len(result.Files) != 1 || result.Files[0].Kind != "part" || len(result.Failures) != 1 {
					t.Fatalf("partial success lost per-file outcomes: %s (%v)", body, err)
				}
			}
			for _, secret := range []string{"probe-user-canary", "probe-password-canary", "probe-query-canary"} {
				if strings.Contains(string(body)+logs(), secret) {
					t.Fatalf("probe response or logs exposed %s", secret)
				}
			}
			if got, err := os.ReadFile(movie.NFOPath); err != nil || !bytes.Equal(got, original) {
				t.Fatal("failed primary probe changed NFO", err)
			}
			if got, err := app.db.Movie(1); err != nil || !reflect.DeepEqual(got, movie) {
				t.Fatal("failed primary probe changed movie", err)
			}
			if _, err := os.Stat(mediaInfoPath(movie.SourcePath)); !os.IsNotExist(err) {
				t.Fatal("failed primary probe created auxiliary output", err)
			}
			assertTrustedSTRMPlayback(t, ts, token, source.String(), &hits)
		})
	}
}

// Both a missing executable and an actual media failure must leave browsing
// and direct playback available, without fetching the redirect destination.
func assertTrustedSTRMPlayback(t *testing.T, ts *httptest.Server, token, source string, hits *atomic.Int64) {
	t.Helper()
	before := hits.Load()
	for _, path := range []string{"/Users/1/Items/1", "/Items/1/PlaybackInfo"} {
		resp, body := embyRaw(t, ts, http.MethodGet, path, token, "")
		if resp.StatusCode != http.StatusOK || !json.Valid(body) {
			t.Fatalf("%s unavailable after probe failure: %d %s", path, resp.StatusCode, body)
		}
	}
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/Videos/1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Emby-Token", token)
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	redirect, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = redirect.Body.Close()
	if redirect.StatusCode != http.StatusFound || redirect.Header.Get("Location") != source || hits.Load() != before {
		t.Fatalf("playback no longer redirects without source access: status=%d hits=%d->%d", redirect.StatusCode, before, hits.Load())
	}
}
