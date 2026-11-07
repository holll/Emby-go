//go:build linux

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

const (
	nativeUpgradeToken  = "native-upgrade-retained-viewer-token"
	nativeUpgradeAPIKey = "native-upgrade-retained-viewer-api-key"
)

// Exercise the same old credentials before upgrade, after each current restart,
// and after restoring the matching old bundle. No login or playback event is
// submitted: these reads must not rewrite the saved auth/history/userdata rows.
func assertNativeUpgradeAPI(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	for _, credential := range []string{nativeUpgradeToken, nativeUpgradeAPIKey} {
		for _, check := range []struct {
			path   string
			status int
		}{
			{"/Users/Me", http.StatusOK},
			{"/Users/2/Items?Recursive=true", http.StatusOK},
			{"/Users/2/Items/1", http.StatusOK},
			{"/Items/1/PlaybackInfo?UserId=2", http.StatusOK},
			{"/Videos/1/stream", http.StatusFound},
			{"/Users/2/Items/2", http.StatusNotFound},
			{"/Videos/2/stream", http.StatusNotFound},
			{"/api/admin/libraries", http.StatusForbidden},
		} {
			req, err := http.NewRequest(http.MethodGet, base+check.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Emby-Token", credential)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", check.path, err)
			}
			raw, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != check.status {
				t.Fatalf("%s: status=%d want=%d read=%v body=%s", check.path, resp.StatusCode, check.status, err, raw)
			}
			switch check.path {
			case "/Users/Me":
				var user struct {
					ID     string `json:"Id"`
					Policy struct{ IsAdministrator bool }
				}
				if err := json.Unmarshal(raw, &user); err != nil || user.ID != "2" || user.Policy.IsAdministrator {
					t.Fatalf("retained credential changed identity/role: %s, %v", raw, err)
				}
			case "/Users/2/Items?Recursive=true":
				var list struct {
					Items []struct {
						ID string `json:"Id"`
					}
					TotalRecordCount int
				}
				if err := json.Unmarshal(raw, &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != "1" || list.TotalRecordCount != 1 {
					t.Fatalf("library scope changed: %s, %v", raw, err)
				}
			case "/Users/2/Items/1":
				var movie struct {
					ID       string `json:"Id"`
					Name     string
					UserData struct {
						PlaybackPositionTicks int64
						PlayCount             int
						IsFavorite            bool
					}
				}
				if err := json.Unmarshal(raw, &movie); err != nil || movie.ID != "1" || movie.Name != "Original" || movie.UserData.PlaybackPositionTicks != 654321 || movie.UserData.PlayCount != 2 || !movie.UserData.IsFavorite {
					t.Fatalf("viewer movie/userdata changed: %s, %v", raw, err)
				}
			case "/Items/1/PlaybackInfo?UserId=2":
				var playback struct {
					MediaSources []struct {
						DirectStreamURL string `json:"DirectStreamUrl"`
					}
					PlaySessionID string `json:"PlaySessionId"`
				}
				if err := json.Unmarshal(raw, &playback); err != nil || len(playback.MediaSources) != 1 || playback.PlaySessionID == "" {
					t.Fatalf("playback contract changed: %s, %v", raw, err)
				}
				streamURL := playback.MediaSources[0].DirectStreamURL
				parsed, err := url.Parse(streamURL)
				if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Path != "/Videos/1/stream" || parsed.Fragment != "" {
					t.Fatalf("playback returned an unexpected stream URL: %q, %v", streamURL, err)
				}
				query := parsed.Query()
				if query.Get("api_key") != credential {
					t.Fatalf("playback URL did not retain the requesting credential: %q", streamURL)
				}
				// Consume the exact returned path/query without an auth header.
				// The client stops at 302 and never contacts the media host.
				stream, err := client.Get(base + streamURL)
				if err != nil {
					t.Fatal(err)
				}
				stream.Body.Close()
				if stream.StatusCode != http.StatusFound || stream.Header.Get("Location") != "https://example.invalid/original.mp4" {
					t.Fatalf("returned playback URL failed: status=%d location=%q", stream.StatusCode, stream.Header.Get("Location"))
				}
				query.Del("api_key")
				parsed.RawQuery = query.Encode()
				anonymous, err := client.Get(base + parsed.String())
				if err != nil {
					t.Fatal(err)
				}
				anonymous.Body.Close()
				if anonymous.StatusCode != http.StatusUnauthorized {
					t.Fatalf("playback URL without credential: status=%d want=401", anonymous.StatusCode)
				}
			case "/Videos/1/stream":
				if got := resp.Header.Get("Location"); got != "https://example.invalid/original.mp4" {
					t.Fatalf("playback redirect changed: %q", got)
				}
			}
		}
	}
	t.Log("retained token/API key: viewer identity, library scope, userdata, PlaybackInfo returned URL query-only exact 302 / missing-key 401, header-auth exact 302 and denied cross-library/admin access verified")
}
