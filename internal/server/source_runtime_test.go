package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/probe"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

func TestMovieRuntimeFollowsSTRMOnLightweightAndFullReads(t *testing.T) {
	for _, tc := range []struct {
		name, nfo string
		after     int64
	}{
		{"probed", "<movie><title>Film</title></movie>", 0},
		{"manual", "<movie><title>Film</title><runtime>2</runtime></movie>", 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Film.strm")
			writeFile(t, path, "https://fixture.invalid/a.mp4\n")
			writeFile(t, filepath.Join(root, "Film.nfo"), tc.nfo)
			a, ts, token := newProbeTestApp(t, root)
			id, err := a.db.MovieIDByPath(path)
			if err != nil {
				t.Fatal(err)
			}
			movie, err := a.db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			url, hash, err := scanner.ReadSourceWithHash(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.writeProbeNFO(movie, url, hash, probe.Info{DurationSeconds: 60, Video: &probe.VideoInfo{Codec: "h264"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := scanner.ReindexOne(a.db, onlyLibrary(t, a), path); err != nil {
				t.Fatal(err)
			}
			const position = 15 * 10000000
			if _, err := a.db.RecordPlaybackEvent(store.PlaybackEvent{UserID: 1, MovieID: id, SessionKey: "runtime-fixture", Kind: store.PlaybackStart, PositionTicks: position, Now: time.Now()}); err != nil {
				t.Fatal(err)
			}
			item := strconv.FormatInt(id, 10)
			list := "/emby/Users/1/Items?Ids=" + item
			for _, endpoint := range []string{list, list + "&Fields=MediaSources", "/emby/Users/1/Items/" + item} {
				check := func(seconds int64) {
					t.Helper()
					code, body := doJSON(t, ts, http.MethodGet, endpoint, token, "")
					var data map[string]any
					if err := json.Unmarshal(body, &data); err != nil || code != http.StatusOK {
						t.Fatalf("%s: %d %s (%v)", endpoint, code, body, err)
					}
					if items, ok := data["Items"].([]any); ok {
						data = items[0].(map[string]any)
					}
					pct := float64(0)
					var ticks any
					if seconds > 0 {
						ticks = float64(seconds * 10000000)
						pct = 1500 / float64(seconds)
					}
					ud := data["UserData"].(map[string]any)
					if data["RunTimeTicks"] != ticks || ud["PlayedPercentage"] != pct || ud["PlaybackPositionTicks"] != float64(position) {
						t.Fatalf("%s runtime=%v user=%+v; want ticks=%v percentage=%v", endpoint, data["RunTimeTicks"], ud, ticks, pct)
					}
				}
				before := tc.after
				if before == 0 {
					before = 60
				}
				writeFile(t, path, "https://fixture.invalid/a.mp4\n")
				check(before)
				writeFile(t, path, "https://fixture.invalid/b.mp4\n")
				check(tc.after) // No scan or cache.Clear between the requests.
			}
		})
	}
}
