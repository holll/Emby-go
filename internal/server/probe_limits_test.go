package server

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/probe"
)

// Valid JSON plus whitespace distinguishes an output-budget failure from a
// decoder failure. The executable does no networking and passes real Verify.
func probeLimitExecutable(t *testing.T, mode string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	dir := t.TempDir()
	good := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360}],"format":{"duration":"12.5","size":"12345"}}`)
	for name, data := range map[string][]byte{
		"good":   good,
		"stdout": append(append([]byte(nil), good...), bytes.Repeat([]byte(" "), (4<<20)+1-len(good))...),
		"stderr": bytes.Repeat([]byte("E"), (64<<10)+1),
		"mode":   []byte(mode),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "ffprobe")
	script := `#!/bin/sh
if [ "$1" = "-version" ]; then printf 'ffprobe version fixture\n'; exit 0; fi
base=$(dirname "$0")
for input do :; done
case "$input" in
  *bad*)
    case "$(cat "$base/mode")" in
      stdout) cat "$base/stdout"; exit 0 ;;
      stderr) cat "$base/stderr" >&2 ;;
      error) printf '%s: Invalid data found when processing input\n' "$input" >&2; exit 1 ;;
    esac ;;
esac
cat "$base/good"
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Join(dir, "mode")
}

// Keep the response-redaction gate deterministic when real ffprobe is absent.
func TestProbeErrorResponseRedactsSourceURL(t *testing.T) {
	app, ts, token := contractEnv(t)
	app.startupWG.Wait()
	binary, _ := probeLimitExecutable(t, "error")
	app.cfg.FFProbePath = binary
	movie, err := app.db.Movie(1)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, movie.SourcePath, "https://probe-user-canary:probe-password-canary@fixture.invalid/bad.mp4?token=probe-query-canary\n")
	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/probe", token, "")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "Invalid data found") || !strings.Contains(string(body), "fixture.invalid/bad.mp4") {
		t.Fatalf("lost useful failure details: %d %s", resp.StatusCode, body)
	}
	for _, secret := range []string{"probe-user-canary", "probe-password-canary", "probe-query-canary"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("probe response exposed %s", secret)
		}
	}
}

func TestProbeOutputLimitsHTTPFailureAndRecovery(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			app, ts, token := contractEnv(t)
			path, modeFile := probeLimitExecutable(t, mode)
			app.cfg.FFProbePath = path
			movie, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, movie.SourcePath, "https://fixture.invalid/bad.mp4\n")
			// Preserve an existing auxiliary artifact too, not only its absence.
			if mode == "stderr" {
				writeFile(t, mediaInfoPath(movie.SourcePath), "existing auxiliary bytes")
			}
			original, err := os.ReadFile(movie.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			aux, auxErr := os.ReadFile(mediaInfoPath(movie.SourcePath))
			if auxErr != nil && !os.IsNotExist(auxErr) {
				t.Fatal(auxErr)
			}
			g, l := app.db.Version("g:version"), app.db.Version(fmt.Sprintf("lib:%d:version", movie.LibraryID))
			logs := captureLogs(t)
			resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/probe", token, "")
			if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "全部文件探测失败") {
				t.Fatalf("probe %d %s", resp.StatusCode, body)
			}
			after, err := app.db.Movie(1)
			if err != nil || !reflect.DeepEqual(movie, after) {
				t.Fatalf("movie changed: %+v err=%v", after, err)
			}
			afterNFO, err := os.ReadFile(movie.NFOPath)
			if err != nil || !bytes.Equal(original, afterNFO) {
				t.Fatal("failed probe changed NFO", err)
			}
			afterAux, afterAuxErr := os.ReadFile(mediaInfoPath(movie.SourcePath))
			if !bytes.Equal(aux, afterAux) || os.IsNotExist(auxErr) != os.IsNotExist(afterAuxErr) || (afterAuxErr != nil && !os.IsNotExist(afterAuxErr)) {
				t.Fatal("failed probe changed auxiliary output", afterAuxErr)
			}
			if app.db.Version("g:version") != g || app.db.Version(fmt.Sprintf("lib:%d:version", movie.LibraryID)) != l || !strings.Contains(logs(), "safe_error=FFPROBE_FAILED") {
				t.Fatal("failure changed versions or lost safe classification", logs())
			}
			if app.nfoBusyOwner() != "" {
				t.Fatal("failure leaked NFO gate")
			}
			writeFile(t, modeFile, "healthy")
			resp, body = embyRaw(t, ts, http.MethodPost, "/api/admin/items/1/probe", token, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("healthy recovery %d %s", resp.StatusCode, body)
			}
			if nfoProbeState(movie.NFOPath).Version != probeVersion {
				t.Fatal("healthy recovery did not save probe marker")
			}
			cached, ok := loadMediaInfo(movie.SourcePath, "https://fixture.invalid/bad.mp4")
			if !ok || len(cached.FFProbe) == 0 {
				t.Fatal("healthy recovery did not save raw output")
			}
		})
	}
}

func TestProbeOutputLimitsBatchContinues(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			app, ts, token := contractEnv(t)
			path, _ := probeLimitExecutable(t, mode)
			app.cfg.FFProbePath, app.cfg.ProbeConcurrency = path, 1
			bad, err := app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, bad.SourcePath, "https://fixture.invalid/bad.mp4\n")
			original, err := os.ReadFile(bad.NFOPath)
			if err != nil {
				t.Fatal(err)
			}
			g, l := app.db.Version("g:version"), app.db.Version(fmt.Sprintf("lib:%d:version", bad.LibraryID))
			logs := captureLogs(t)
			if err := app.beginProbe(1); err != nil {
				t.Fatal(err)
			}
			if err := app.runProbe(app.startTask("probe"), path, []probeTarget{{Movie: bad, Path: bad.SourcePath, Primary: true, Index: 1}}, false); err != nil {
				t.Fatal(err)
			}
			app.probeTaskMu.RLock()
			first := app.probeStatus
			app.probeTaskMu.RUnlock()
			if first.Done != 1 || first.Failed != 1 || first.Success != 0 || first.Running || first.Aborted || first.Cancelled || !strings.Contains(logs(), "safe_error=FFPROBE_FAILED") {
				t.Fatalf("failed-only status=%+v logs=%s", first, logs())
			}
			if app.db.Version("g:version") != g || app.db.Version(fmt.Sprintf("lib:%d:version", bad.LibraryID)) != l {
				t.Fatal("failed-only batch published versions")
			}
			if data, err := os.ReadFile(bad.NFOPath); err != nil || !bytes.Equal(data, original) {
				t.Fatal("failed-only batch changed NFO", err)
			}
			if _, err := os.Stat(mediaInfoPath(bad.SourcePath)); !os.IsNotExist(err) {
				t.Fatal("failed-only batch saved raw output", err)
			}
			// The second target follows the failed one on one worker: no failure
			// retry or early abort may prevent its normal commit.
			healthySource := filepath.Join(filepath.Dir(bad.SourcePath), "ZZZ-999.strm")
			writeFile(t, healthySource, "https://fixture.invalid/healthy.mp4\n")
			writeFile(t, strings.TrimSuffix(healthySource, ".strm")+".nfo", "<movie><title>Healthy</title><num>ZZZ-999</num></movie>")
			scanOnce(t, ts, token)
			bad, err = app.db.Movie(1)
			if err != nil {
				t.Fatal(err)
			}
			g, l = app.db.Version("g:version"), app.db.Version(fmt.Sprintf("lib:%d:version", bad.LibraryID))
			resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/probe/media", token, `{"only_missing":false}`)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("batch startup %d %s", resp.StatusCode, body)
			}
			done := make(chan struct{})
			go func() { app.workerWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("batch did not join")
			}
			app.probeTaskMu.RLock()
			status := app.probeStatus
			app.probeTaskMu.RUnlock()
			if status.Done != 2 || status.Total != 2 || status.Success != 1 || status.Failed != 1 || status.Skipped != 0 || status.Aborted || status.Cancelled || status.Running {
				t.Fatalf("mixed batch=%+v", status)
			}
			if data, err := os.ReadFile(bad.NFOPath); err != nil || !bytes.Equal(data, original) {
				t.Fatal("mixed batch changed failed NFO", err)
			}
			if _, err := os.Stat(mediaInfoPath(bad.SourcePath)); !os.IsNotExist(err) {
				t.Fatal("mixed batch saved failed raw output", err)
			}
			after, err := app.db.Movie(1)
			if err != nil || !reflect.DeepEqual(bad, after) {
				t.Fatalf("batch changed failed DB movie: %+v %v", after, err)
			}
			if nfoProbeState(strings.TrimSuffix(healthySource, ".strm")+".nfo").Version != probeVersion {
				t.Fatal("subsequent healthy target not processed")
			}
			if cached, ok := loadMediaInfo(healthySource, "https://fixture.invalid/healthy.mp4"); !ok || len(cached.FFProbe) == 0 {
				t.Fatal("healthy target raw output not saved")
			}
			gv, _ := strconv.Atoi(g)
			lv, _ := strconv.Atoi(l)
			if app.db.Version("g:version") != strconv.Itoa(gv+1) || app.db.Version(fmt.Sprintf("lib:%d:version", bad.LibraryID)) != strconv.Itoa(lv+1) {
				t.Fatal("mixed batch did not publish exactly one successful library refresh")
			}
			if app.nfoBusyOwner() != "" {
				t.Fatal("mixed batch leaked NFO gate")
			}
			if err := probe.Verify(app.rootCtx, path); err != nil {
				t.Fatal("subprocess budget not reusable after failure", err)
			}
		})
	}
}
