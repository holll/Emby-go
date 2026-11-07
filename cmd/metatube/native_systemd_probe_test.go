//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

// admitNativeSystemdProbe returns assertions to run after the owning test has
// stopped its unit, before any fallback cleanup can make the assertions pass.
func admitNativeSystemdProbe(t *testing.T, base, state, runtime string, mainPID int) func() {
	t.Helper()
	media := filepath.Join(state, "media")
	if err := os.Mkdir(media, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(media, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{
		filepath.Join(media, "a.strm"): []byte("https://fixture.invalid/systemd.mp4\n"),
		filepath.Join(media, "a.nfo"):  []byte("<movie><title>Systemd signal fixture</title><customtag>retain</customtag></movie>"),
	}
	for path, data := range original {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(state, "data.db")
	before := launchNativeSystemdProbe(t, base, media, dbPath)
	pids := awaitNativeSystemdProbeTree(t, filepath.Join(runtime, "probe-pids"), mainPID)
	return func() {
		t.Helper()
		for _, pid := range pids {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			state := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])[0]
			if state != "Z" && state != "X" {
				t.Fatalf("probe %d remains live after successful systemd stop: %s", pid, raw)
			}
		}
		for path, want := range original {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("cancelled probe changed %s: %v %s", path, err, got)
			}
		}
		if entries, err := os.ReadDir(media); err != nil || len(entries) != 2 {
			t.Fatalf("cancelled probe left auxiliary media output: %v %v", entries, err)
		}
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		after, err := db.Movie(before.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("cancelled probe changed movie: before=%+v after=%+v err=%v", before, after, err)
		}
	}
}

// launchNativeSystemdProbe uses the public initialization/scan/probe endpoints,
// retaining the indexed movie before the admitted background operation starts.
func launchNativeSystemdProbe(t *testing.T, base, media, dbPath string) store.Movie {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	token := ""
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
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
			t.Fatalf("%s: status=%d err=%v body=%s", path, resp.StatusCode, err, raw)
		}
		var result map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	call("/api/auth/initialize", `{"Username":"admin","Pw":"password-1234","Code":"native-fixture"}`, http.StatusNoContent)
	auth := call("/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`, http.StatusOK)
	token, _ = auth["AccessToken"].(string)
	if token == "" {
		t.Fatalf("missing fixture token: %v", auth)
	}
	library, err := json.Marshal(map[string]string{"Name": "Systemd", "Path": media})
	if err != nil {
		t.Fatal(err)
	}
	call("/api/admin/libraries", string(library), http.StatusOK)
	if scan := call("/api/admin/scan", "", http.StatusOK); scan["success"] != float64(1) {
		t.Fatalf("scan=%v", scan)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.MovieIDByPath(filepath.Join(media, "a.strm"))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	before, err := db.Movie(id)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("capture movie: %v close=%v", err, closeErr)
	}
	status := call("/api/admin/probe/media", `{"only_missing":false}`, http.StatusAccepted)
	if status["total"] != float64(1) {
		t.Fatalf("expected exactly one admitted probe: %v", status)
	}
	return before
}

// awaitNativeSystemdProbeTree establishes non-vacuous process evidence before
// stop: live parent/child, a separate probe group, unit membership and TERM ignored.
func awaitNativeSystemdProbeTree(t *testing.T, marker string, mainPID int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var pids []int
	for {
		raw, err := os.ReadFile(marker)
		if err == nil && len(strings.Fields(string(raw))) == 2 {
			for _, field := range strings.Fields(string(raw)) {
				pid, err := strconv.Atoi(field)
				if err != nil || pid <= 1 || pid == mainPID {
					t.Fatalf("invalid fixture PID %q: %v", field, err)
				}
				pids = append(pids, pid)
			}
			break
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("admitted systemd probe did not launch its process tree")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mainCgroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", mainPID))
	if err != nil {
		t.Fatal(err)
	}
	for index, pid := range pids {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
		parent := mainPID
		if index == 1 {
			parent = pids[0]
		}
		if fields[0] == "Z" || fields[0] == "X" || fields[1] != strconv.Itoa(parent) || fields[2] != strconv.Itoa(pids[0]) {
			t.Fatalf("probe is not a live, separately grouped parent/child tree: pid=%d %s", pid, raw)
		}
		cgroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
		if err != nil || !bytes.Equal(cgroup, mainCgroup) {
			t.Fatalf("probe %d escaped the fixture service cgroup: %s %v", pid, cgroup, err)
		}
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			t.Fatal(err)
		}
		_, ignored, found := strings.Cut(string(status), "SigIgn:\t")
		if !found {
			t.Fatalf("missing SigIgn for fixture %d", pid)
		}
		mask, err := strconv.ParseUint(strings.Fields(ignored)[0], 16, 64)
		if err != nil || mask&(1<<14) == 0 { // Linux SIGTERM is signal 15.
			t.Fatalf("fixture %d does not ignore systemd's initial SIGTERM: %v %s", pid, err, status)
		}
	}
	t.Logf("admitted probe tree %v is live in the app cgroup and ignores SIGTERM", pids)
	return pids
}
