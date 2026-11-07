//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

// assertNativeSystemdRealProbe exercises the service's PATH-resolved ffprobe,
// writable media directory, and public playback after the cancelled fake probe.
func assertNativeSystemdRealProbe(t *testing.T, base, state string) {
	t.Helper()
	// The service cannot read this checkout under ProtectHome. Serve bytes from
	// the test process instead of resolving a fixture path inside its sandbox.
	sample, err := os.ReadFile("../../internal/server/testdata/probe-sample.mp4")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	mediaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe-sample.mp4" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "probe-sample.mp4", time.Time{}, bytes.NewReader(sample))
	}))
	defer mediaServer.Close()
	source := mediaServer.URL + "/probe-sample.mp4"
	media := filepath.Join(state, "real-media")
	if err := os.Mkdir(media, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(media, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	strmPath, nfoPath := filepath.Join(media, "real.strm"), filepath.Join(media, "real.nfo")
	const title = "Systemd real probe manual title"
	// Seed stale technical duration so the later explicit rescan must change
	// the index; this half-second sample truncates to zero whole seconds.
	originalNFO := []byte("<movie><title>" + title + "</title><customtag>retain-real</customtag><fileinfo><streamdetails><video><durationinseconds>7</durationinseconds></video></streamdetails></fileinfo></movie>")
	for path, data := range map[string][]byte{strmPath: []byte(source + "\n"), nfoPath: originalNFO} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}

	client := &http.Client{
		Timeout: 75 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	token := ""
	call := func(method, path, body string, want int) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d err=%v body=%s", method, path, resp.StatusCode, want, err, raw)
		}
		return resp, raw
	}
	_, raw := call(http.MethodPost, "/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`, http.StatusOK)
	var auth struct {
		AccessToken string
		User        struct{ ID string }
	}
	if err := json.Unmarshal(raw, &auth); err != nil || auth.AccessToken == "" || auth.User.ID == "" {
		t.Fatalf("authenticate existing admin: %v %s", err, raw)
	}
	token = auth.AccessToken
	library, err := json.Marshal(map[string]string{"Name": "Systemd real probe", "Path": media})
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "/api/admin/libraries", string(library), http.StatusOK)
	_, raw = call(http.MethodPost, "/api/admin/scan", "", http.StatusOK)
	var scan struct{ Success, Failed int }
	if err := json.Unmarshal(raw, &scan); err != nil || scan.Success != 2 || scan.Failed != 0 {
		t.Fatalf("scan existing and real libraries: %v %s", err, raw)
	}
	db, err := store.Open(filepath.Join(state, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	movieID, err := db.MovieIDByPath(strmPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Movie(movieID)
	if err != nil || before.Title != title || before.RuntimeSeconds != 7 {
		t.Fatalf("unprobed movie: %+v err=%v", before, err)
	}
	id := strconv.FormatInt(movieID, 10)
	_, raw = call(http.MethodPost, "/api/admin/items/"+id+"/probe", "", http.StatusOK)
	var result struct {
		ID      int64  `json:"id"`
		NFOPath string `json:"nfo_path"`
		Info    struct {
			Duration int64 `json:"duration_seconds"`
			Size     int64 `json:"size_bytes"`
			Video    struct {
				Codec         string
				Width, Height int
			}
		}
		Files    []json.RawMessage
		Failures []string
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.ID != movieID || result.NFOPath != nfoPath || len(result.Files) != 1 || len(result.Failures) != 0 || result.Info.Video.Codec != "h264" || result.Info.Video.Width != 320 || result.Info.Video.Height != 240 || result.Info.Duration != 0 || result.Info.Size != int64(len(sample)) {
		t.Fatalf("real single-item probe response: %v %s", err, raw)
	}
	if hits.Load() == 0 {
		t.Fatal("successful probe did not request the loopback HTTP source")
	}
	updated, err := os.ReadFile(nfoPath)
	if err != nil || bytes.Equal(updated, originalNFO) || !bytes.Contains(updated, []byte("<customtag>retain-real</customtag>")) {
		t.Fatalf("probe must update NFO and retain custom tag: %v %s", err, updated)
	}
	meta, err := nfo.Read(nfoPath)
	if err != nil || meta.Title != title || meta.FileInfo == nil || meta.FileInfo.StreamDetails == nil || meta.FileInfo.StreamDetails.Video == nil {
		t.Fatalf("probed NFO: %+v err=%v", meta, err)
	}
	video := meta.FileInfo.StreamDetails.Video
	if video.Codec != "h264" || video.Width != 320 || video.Height != 240 || video.DurationSeconds != result.Info.Duration || meta.FileInfo.Size != int64(len(sample)) || meta.FileInfo.ProbeVersion != 1 || meta.FileInfo.ProbeURL != source {
		t.Fatalf("persisted NFO video=%+v fileinfo=%+v", video, meta.FileInfo)
	}

	sidecarRaw, err := os.ReadFile(filepath.Join(media, "mediainfo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar struct {
		ProbeVersion int    `json:"probe_version"`
		Source       string `json:"source"`
		MediaFile    string `json:"media_file"`
		FFProbe      struct {
			Streams []struct {
				CodecName     string `json:"codec_name"`
				Width, Height int
			}
			Format struct {
				Duration string `json:"duration"`
			}
		} `json:"ffprobe"`
	}
	if err := json.Unmarshal(sidecarRaw, &sidecar); err != nil || sidecar.ProbeVersion != 1 || sidecar.Source != source || sidecar.MediaFile != "probe-sample.mp4" || len(sidecar.FFProbe.Streams) != 2 || sidecar.FFProbe.Streams[0].CodecName != "h264" || sidecar.FFProbe.Streams[0].Width != 320 || sidecar.FFProbe.Streams[0].Height != 240 || sidecar.FFProbe.Format.Duration != "0.500000" {
		t.Fatalf("raw ffprobe sidecar: %v %s", err, sidecarRaw)
	}
	indexed, err := db.Movie(movieID)
	if err != nil || indexed.RuntimeSeconds != 7 {
		t.Fatalf("synchronous probe unexpectedly refreshed index: %+v err=%v", indexed, err)
	}
	// Probe writes NFO, not the index. A synchronous scan replaces the stale
	// duration; codec/dimensions remain NFO-backed, not Movie fields.
	_, raw = call(http.MethodPost, "/api/admin/scan", "", http.StatusOK)
	if err := json.Unmarshal(raw, &scan); err != nil || scan.Success != 2 || scan.Failed != 0 {
		t.Fatalf("rescan probed metadata: %v %s", err, raw)
	}
	after, err := db.Movie(movieID)
	if err != nil || after.Title != title || after.SourcePath != strmPath || after.NFOPath != nfoPath || after.RuntimeSeconds != result.Info.Duration || after.SourceProtocol != "http" || after.SourceContainer != "mp4" {
		t.Fatalf("persisted movie metadata: %+v err=%v", after, err)
	}
	_, raw = call(http.MethodGet, "/Users/"+auth.User.ID+"/Items/"+id, "", http.StatusOK)
	var item struct {
		ID, Name     string
		RunTimeTicks int64
	}
	if err := json.Unmarshal(raw, &item); err != nil || item.ID != id || item.Name != title || item.RunTimeTicks != after.RuntimeSeconds*10_000_000 {
		t.Fatalf("browse probed movie: %v %s", err, raw)
	}
	_, raw = call(http.MethodGet, "/Items/"+id+"/PlaybackInfo", "", http.StatusOK)
	var playback struct {
		MediaSources []struct {
			SupportsDirectPlay bool
			MediaStreams       []struct {
				Type, Codec   string
				Width, Height int
			}
		}
	}
	if err := json.Unmarshal(raw, &playback); err != nil || len(playback.MediaSources) != 1 {
		t.Fatalf("PlaybackInfo sources: %v %s", err, raw)
	}
	playSource := playback.MediaSources[0]
	if !playSource.SupportsDirectPlay || len(playSource.MediaStreams) != 2 || playSource.MediaStreams[0].Type != "Video" || playSource.MediaStreams[0].Codec != "h264" || playSource.MediaStreams[0].Width != 320 || playSource.MediaStreams[0].Height != 240 {
		t.Fatalf("PlaybackInfo probed video: %s", raw)
	}
	beforeRedirect := hits.Load()
	resp, _ := call(http.MethodGet, "/Videos/"+id+"/stream", "", http.StatusFound)
	if got := resp.Header.Get("Location"); got != source {
		t.Fatalf("stream Location=%q want exact source %q", got, source)
	}
	if hits.Load() != beforeRedirect {
		t.Fatal("stream redirect was followed or fetched the upstream source")
	}
	t.Logf("real systemd ffprobe fetched HTTP source %d times; persisted h264 320x240, duration=%ds size=%d and retained manual metadata; browse/PlaybackInfo/302 verified", hits.Load(), after.RuntimeSeconds, len(sample))
}
