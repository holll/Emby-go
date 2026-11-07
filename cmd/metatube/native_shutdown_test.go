//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"emby-go/internal/store"
)

// stopNativeFixtureSession is failure-path fixture hygiene, not a production
// shutdown mechanism. Setpgid does not escape the session created by Setsid.
func stopNativeFixtureSession(t *testing.T, sessionID int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		paths, err := filepath.Glob("/proc/[0-9]*/stat")
		if err != nil {
			t.Fatal(err)
		}
		live := 0
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Errorf("fixture session discovery: %v", err)
				continue
			}
			fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
			if fields[3] != strconv.Itoa(sessionID) || fields[0] == "Z" || fields[0] == "X" {
				continue
			}
			live++
			pid, err := strconv.Atoi(filepath.Base(filepath.Dir(path)))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("fixture kill %d: %v", pid, err)
			}
		}
		if live == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("fixture session %d still has %d live members", sessionID, live)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNativeFixtureSessionCleanup(t *testing.T) {
	// Deliberately provide no PID marker to cleanup. The shell and its child
	// must both be found by session membership, not a successfully parsed file.
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stopNativeFixtureSession(t, cmd.Process.Pid); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Wait for the child to exist so this cannot pass on the shell alone.
	deadline := time.Now().Add(time.Second)
	for {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		if len(strings.Fields(string(raw))) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup fixture did not spawn child")
		}
		time.Sleep(time.Millisecond)
	}
	stopNativeFixtureSession(t, cmd.Process.Pid)
}

// assertNativeShutdownAdmission holds an authenticated request at the body-read
// boundary using HTTP's 100-continue handshake, then releases it after SIGTERM.
// This exercises the real listener and handler gate without a production hook.
func assertNativeShutdownAdmission(t *testing.T, base, token string, process *os.Process) time.Time {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	const body = `{"only_missing":false}`
	if _, err := fmt.Fprintf(conn, "POST /api/admin/probe/media HTTP/1.1\r\nHost: fixture\r\nX-Emby-Token: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", token, len(body)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	interim, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = interim.Body.Close()
	if interim.StatusCode != http.StatusContinue {
		t.Fatalf("handler did not enter request-body read: %s", interim.Status)
	}
	started := time.Now()
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	// A pending body keeps HTTP drain alive while new readiness requests fail.
	// A closed listener is also externally not-ready; it is not an HTTP 503.
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := started.Add(3 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err != nil {
			if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ECONNRESET) && !errors.Is(err, io.EOF) {
				t.Fatalf("unexpected readiness failure: %v", err)
			}
			t.Log("SIGTERM readiness externally unavailable: listener refused/closed connection (not an observed HTTP 503)")
			break
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			var health struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &health); err != nil || health.Status != "shutting_down" {
				t.Fatalf("shutdown readiness: %s %v", raw, err)
			}
			t.Log("SIGTERM readiness returned HTTP 503 shutting_down")
			break
		}
		if resp.StatusCode != http.StatusOK || time.Now().After(deadline) {
			t.Fatalf("readiness remained available or unexpected: %d %s", resp.StatusCode, raw)
		}
		time.Sleep(time.Millisecond)
	}
	if err := conn.SetDeadline(started.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, body); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("pending request was not drained: %v", err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var rejected struct {
		Error string `json:"error"`
	}
	decodeErr := json.Unmarshal(raw, &rejected)
	if readErr != nil || decodeErr != nil || resp.StatusCode != http.StatusServiceUnavailable || rejected.Error != "server shutting down" {
		t.Fatalf("pending probe: status=%d read=%v decode=%v body=%s", resp.StatusCode, readErr, decodeErr, raw)
	}
	t.Log("SIGTERM pending authenticated probe: 100-continue before signal, completed body after readiness failed, HTTP 503 server shutting down")
	return started
}

// Exercise OS signal -> main -> admitted background probe -> process group,
// rather than invoking BeginShutdown or cancelling probe.Run directly.
func TestNativeBinarySIGTERMActiveProbe(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "emby-go")
	buildNativeReleaseFixture(t, binary)
	marker := filepath.Join(root, "probe-pids")
	launcher := filepath.Join(root, "ffprobe-fixture")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\n echo 'ffprobe version fixture'\n exit 0\nfi\nsleep 300 &\nchild=$!\nprintf '%%s %%s\\n' \"$$\" \"$child\" > %q\nwait \"$child\"\n", marker)
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(media, "a.strm")
	nfo := filepath.Join(media, "a.nfo")
	original := []byte("<movie><title>Native signal fixture</title><customtag>retain</customtag></movie>")
	if err := os.WriteFile(source, []byte("https://fixture.invalid/native.mp4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfo, original, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "data.db")
	config := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("port: %d\ndb_path: %q\nredis_addr: ''\nsetup_code: native-fixture\nffprobe_path: %q\nprobe_timeout_seconds: 60\nprobe_concurrency: 1\n", port, dbPath, launcher)), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cmd := exec.Command(binary, "-c", config)
	cmd.Dir = root
	// Probe creates its own process group but stays in this fixture session.
	// Failure cleanup can therefore find it even before the PID marker exists.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	joined := false
	var pids []int
	defer func() {
		// Success assertions above this deferred fallback remain the evidence.
		// Discover all session members, including independently grouped probes
		// whose PID marker was never read, and wait until none remain live.
		stopNativeFixtureSession(t, cmd.Process.Pid)
		if !joined {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-done:
			joined = true
			t.Fatalf("startup: %v\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
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
		t.Fatalf("missing token: %v", auth)
	}
	lib, err := json.Marshal(map[string]string{"Name": "Native", "Path": media})
	if err != nil {
		t.Fatal(err)
	}
	call("/api/admin/libraries", string(lib), http.StatusOK)
	scan := call("/api/admin/scan", "", http.StatusOK)
	if scan["success"] != float64(1) {
		t.Fatalf("scan=%v", scan)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.MovieIDByPath(source)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	before, err := db.Movie(id)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	status := call("/api/admin/probe/media", `{"only_missing":false}`, http.StatusAccepted)
	if status["total"] != float64(1) {
		t.Fatalf("not exactly one admitted probe: %v", status)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(marker)
		if err == nil && len(strings.Fields(string(raw))) == 2 {
			for _, field := range strings.Fields(string(raw)) {
				pid, err := strconv.Atoi(field)
				if err != nil || pid <= 1 {
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
			t.Fatal("admitted probe did not launch process tree")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Both must still be alive before the signal; otherwise cleanup could pass vacuously.
	for _, pid := range pids {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		state := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])[0]
		if state == "Z" || state == "X" {
			t.Fatalf("fixture %d already dead", pid)
		}
	}
	started := assertNativeShutdownAdmission(t, base, token, cmd.Process)
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatalf("shutdown: %v\n%s", err, logs.String())
		}
	case <-time.After(17*time.Second - time.Since(started)):
		t.Fatal("active probe exceeded shared shutdown budget plus slack")
	}
	if !strings.Contains(logs.String(), "已退出") || strings.Contains(logs.String(), "强制退出") {
		t.Fatalf("not graceful: %s", logs.String())
	}
	t.Logf("SIGTERM with active probe joined in %s", time.Since(started))
	for _, pid := range pids {
		deadline := time.Now().Add(time.Second)
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			// Dead orphan zombies may await host-init reaping; they are not live work.
			state := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])[0]
			if state == "Z" || state == "X" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("process %d remains live: %s", pid, raw)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if raw, err := os.ReadFile(nfo); err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("cancelled probe changed NFO: %v %s", err, raw)
	}
	entries, err := os.ReadDir(media)
	if err != nil || len(entries) != 2 {
		t.Fatalf("probe left auxiliary output: %v %v", entries, err)
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.Movie(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("cancelled probe changed movie: before=%+v after=%+v err=%v", before, after, err)
	}
}
