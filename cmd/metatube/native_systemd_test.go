//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"emby-go/internal/store"
)

// assertNativeSystemdProcess inspects the actual service, not only unit properties.
func assertNativeSystemdProcess(t *testing.T, pid int, paths map[string]string) {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Uid:\t65534\t65534\t65534\t65534\n", "Gid:\t65534\t65534\t65534\t65534\n", "NoNewPrivs:\t1\n"} {
		if !strings.Contains(string(status), want) {
			t.Fatalf("missing process identity/sandbox status %q: %s", want, status)
		}
	}
	mounts, err := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", pid))
	if err != nil {
		t.Fatal(err)
	}
	// Resolve the longest mount prefix: ProtectSystem need not create a mount
	// at the fixture root itself. Generated fixture paths contain no escapes.
	for path, mode := range paths {
		longest, options := "", ""
		for _, line := range strings.Split(string(mounts), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}
			mount := fields[4]
			if (path == mount || strings.HasPrefix(path, strings.TrimSuffix(mount, "/")+"/")) && len(mount) > len(longest) {
				longest, options = mount, fields[5]
			}
		}
		if !strings.Contains(","+options+",", ","+mode+",") {
			t.Fatalf("%s effective mount %s options=%q want %s", path, longest, options, mode)
		}
	}
	hostTmp, err := os.Stat("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	privateTmp, err := os.Stat(fmt.Sprintf("/proc/%d/root/tmp", pid))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(hostTmp, privateTmp) {
		t.Fatal("PrivateTmp did not isolate the service /tmp from the host")
	}
	hiddenHome, err := os.Stat(fmt.Sprintf("/proc/%d/root/root", pid))
	if err != nil || hiddenHome.Mode().Perm() != 0 {
		t.Fatalf("ProtectHome did not replace /root with an inaccessible directory: %v %v", hiddenHome, err)
	}
}

// assertNativeSystemdFailureRestart kills only the verified fixture MainPID,
// then requires a new healthy application and the manager's signal-exit evidence.
func assertNativeSystemdFailureRestart(t *testing.T, run func(string, ...string) string, unit string, pid int, base, serverID, exitPath string) int {
	t.Helper()
	started := time.Now()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	var nextPID int
	for {
		candidate, err := strconv.Atoi(strings.TrimSpace(run("systemctl", "show", unit, "--property=MainPID", "--value")))
		if err != nil {
			t.Fatal(err)
		}
		if candidate > 0 && candidate != pid {
			resp, err := client.Get(base + "/healthz")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					nextPID = candidate
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not automatically restart after SIGKILL")
		}
		time.Sleep(20 * time.Millisecond)
	}
	exit, err := os.ReadFile(exitPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Result=signal\n", "ExecMainCode=2\n", "ExecMainStatus=9\n"} {
		if !strings.Contains(string(exit), want) {
			t.Fatalf("missing actual SIGKILL exit evidence %q: %s", want, exit)
		}
	}
	if got := strings.TrimSpace(run("systemctl", "show", unit, "--property=NRestarts", "--value")); got != "1" {
		t.Fatalf("expected exactly one automatic restart, got %q", got)
	}
	policy := run("systemctl", "show", unit, "--property=Restart", "--property=RestartUSec")
	if !strings.Contains(policy, "Restart=on-failure\n") || !strings.Contains(policy, "RestartUSec=3s\n") {
		t.Fatalf("unexpected effective restart policy: %s", policy)
	}
	resp, err := client.Get(base + "/System/Info/Public")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || body.ID != serverID {
		t.Fatalf("server ID changed after automatic restart: %q -> %q %v", serverID, body.ID, err)
	}
	t.Logf("SIGKILL pid=%d restarted as pid=%d after %s; %s", pid, nextPID, time.Since(started), exit)
	return nextPID
}

// nativeSystemdServiceProperties translates the shipped, simple Service section
// to systemd-run properties. This is deliberately not a general unit-file parser.
func nativeSystemdServiceProperties(unitTemplate []byte) ([]string, error) {
	var properties []string
	seen := make(map[string]bool)
	section := ""
	for _, line := range strings.Split(string(unitTemplate), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[Service]" {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		// Repeated list/reset directives need deliberate fixture adaptation:
		// otherwise a second StateDirectory could escape the unique path override.
		if !ok || strings.HasSuffix(line, "\\") || seen[name] {
			return nil, fmt.Errorf("service fixture needs adaptation for unsupported template line %q", line)
		}
		seen[name] = true
		properties = append(properties, line)
	}
	if !slices.Contains(properties, "ExecStart=/opt/emby-go/emby-go -c /etc/emby-go/config.yaml") {
		return nil, fmt.Errorf("deployment ExecStart changed; adapt the fixture command before rehearsing it")
	}
	return properties, nil
}

func TestNativeSystemdServiceTemplate(t *testing.T) {
	content, err := os.ReadFile("../../deploy/emby-go.service")
	if err != nil {
		t.Fatal(err)
	}
	properties, err := nativeSystemdServiceProperties(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=root", "Group=root", "WorkingDirectory=/opt/emby-go", "Restart=always", "TimeoutStopSec=30s", "KillMode=control-group", "ReadWritePaths=/opt/emby-go /etc/emby-go"} {
		if !slices.Contains(properties, want) {
			t.Fatalf("missing approved deployment property %q: %v", want, properties)
		}
	}
	for _, tc := range []struct {
		name, old, replacement, want string
	}{
		{"duplicate-property", "User=root", "User=root\nUser=not-our-fixture", "unsupported template line"},
		{"continuation", "User=root", "User=root\\", "unsupported template line"},
		{"malformed", "User=root", "User", "unsupported template line"},
		{"changed-command", "ExecStart=/opt/emby-go/emby-go", "ExecStart=/other/emby-go", "deployment ExecStart changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(string(content), tc.old, tc.replacement, 1)
			_, err := nativeSystemdServiceProperties([]byte(changed))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsupported template accepted or misclassified: %v", err)
			}
		})
	}
}

// This is an opt-in local manager rehearsal, not the installed release unit.
// Keep all paths and unit names separate from the real installation.
func TestNativeSystemdSandbox(t *testing.T) {
	if os.Getenv("EMBY_GO_TEST_SYSTEMD") != "1" {
		t.Skip("set EMBY_GO_TEST_SYSTEMD=1 to exercise a real root systemd manager")
	}
	if os.Geteuid() != 0 {
		t.Fatal("enabled systemd rehearsal requires root")
	}
	run := func(program string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, program, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", program, args, err, out)
		}
		return string(out)
	}
	if got := strings.TrimSpace(run("systemctl", "is-system-running")); got != "running" {
		t.Fatalf("systemd manager not running: %q", got)
	}
	// Rehearse the shipped service settings, not a second handwritten copy.
	// Only identity and paths are substituted below; Unit/Install dependencies
	// are intentionally not applied to the transient, explicitly started unit.
	unitTemplate, err := os.ReadFile("../../deploy/emby-go.service")
	if err != nil {
		t.Fatal(err)
	}
	serviceProperties, err := nativeSystemdServiceProperties(unitTemplate)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("deployment service SHA256=%x: %d Service directives; only four identity/path substitutions plus fixture command and instrumentation", sha256.Sum256(unitTemplate), len(serviceProperties))
	root, err := os.MkdirTemp("/run", "emby-go-systemd-")
	if err != nil {
		t.Fatal(err)
	}
	// Unique sibling names let systemd own the real /var/lib and /run directory
	// lifecycle, without touching emby-go's installed directories.
	directoryName := filepath.Base(root) + "-managed"
	state := filepath.Join("/var/lib", directoryName)
	runtime := filepath.Join("/run", directoryName)
	for _, path := range []string{state, runtime} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			// root is the still-empty directory returned by MkdirTemp above;
			// the colliding managed path is not ours and must never be removed.
			_ = os.Remove(root)
			t.Fatalf("managed fixture directory must not preexist: %s %v", path, err)
		}
	}
	var install string
	var units []string
	t.Cleanup(func() {
		// Stop only our own units, and retain fixtures if manager state cannot
		// confirm they are no longer active. Never delete files under a live unit.
		for _, unit := range units {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			_ = exec.CommandContext(ctx, "systemctl", "stop", unit).Run()
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).CombinedOutput()
			cancel()
			state := strings.TrimSpace(string(out))
			if state != "inactive" && state != "failed" && state != "unknown" {
				t.Errorf("cannot confirm fixture unit stopped: %s %q %v; retaining directories", unit, state, err)
				return
			}
		}
		for path, parent := range map[string]string{root: "/run", install: "/tmp", state: "/var/lib", runtime: "/run"} {
			if path == "" { // install creation can fail after root was created.
				continue
			}
			absolute, err := filepath.Abs(path)
			if err != nil || absolute != path || filepath.Dir(path) != parent || !strings.HasPrefix(filepath.Base(path), "emby-go-systemd-") {
				t.Errorf("refusing unexpected fixture cleanup path %q: %v", path, err)
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				t.Error(err)
			}
		}
	})
	// /run may be noexec. Use a separate executable /tmp installation and an
	// explicit read-only bind into PrivateTmp, rather than remounting the host.
	install, err = os.MkdirTemp("/tmp", "emby-go-systemd-")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, install} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 平铺模板不再使用 StateDirectory/RuntimeDirectory：夹具自己建相同权限的
	// 数据/运行目录，继续验证 ProtectSystem/ReadWritePaths 下的写入边界。
	for _, dir := range []string{state, runtime} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(install, "emby-go")
	buildNativeReleaseFixture(t, binary)
	configDir := filepath.Join(root, "etc")
	if err := os.Mkdir(configDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(configDir, 0, 65534); err != nil {
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
	configPath, dbPath := filepath.Join(configDir, "config.yaml"), filepath.Join(state, "data.db")
	launcher := filepath.Join(install, "ffprobe-fixture")
	probeMarker := filepath.Join(runtime, "probe-pids")
	// Ignoring TERM prevents systemd's initial cgroup SIGTERM from making the
	// active-probe check pass without application cancellation/group cleanup.
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\n echo 'ffprobe version fixture'\n exit 0\nfi\ntrap '' TERM\nsleep 300 &\nchild=$!\nprintf '%%s %%s\\n' \"$$\" \"$child\" > %q\nwait \"$child\"\n", probeMarker)
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("port: %d\ndb_path: %q\nredis_addr: ''\nsetup_code: native-fixture\nffprobe_path: %q\nprobe_timeout_seconds: 60\nprobe_concurrency: 1\n", port, dbPath, launcher)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(configPath, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var serverID string
	iterations := 3
	realProbePath := ""
	if configured := os.Getenv("EMBY_GO_TEST_FFPROBE"); configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			t.Fatal(err)
		}
		realProbePath, err = filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		iterations++
	}
	for iteration := range iterations {
		if iteration == 3 {
			// The preceding unit has stopped. Preserve its persisted identity and
			// credentials, changing only the executable setting for a PATH lookup.
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(data), "\n")
			found := false
			for i, line := range lines {
				if strings.HasPrefix(line, "ffprobe_path:") {
					lines[i] = "ffprobe_path: ffprobe"
					found = true
				}
			}
			if !found {
				t.Fatal("persisted config lost ffprobe_path")
			}
			if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		unit := fmt.Sprintf("emby-go-rehearsal-%d-%d-%d.service", os.Getpid(), time.Now().UnixNano(), iteration)
		units = append(units, unit)
		exitPath := filepath.Join(state, fmt.Sprintf("exit-%d", iteration))
		args := []string{"--quiet", "--collect", "--unit=" + unit}
		overrides := map[string]string{
			"User": "65534", "Group": "65534", "WorkingDirectory": state,
			"ReadWritePaths": state + " " + runtime + " " + configPath,
		}
		for _, property := range serviceProperties {
			name, _, _ := strings.Cut(property, "=")
			if name == "ExecStart" {
				continue // The checked template command uses our binary/config paths.
			}
			if value, ok := overrides[name]; ok {
				property = name + "=" + value
				delete(overrides, name)
			}
			args = append(args, "--property="+property)
		}
		if len(overrides) != 0 {
			t.Fatalf("deployment service lost fixture substitution directives: %v", overrides)
		}
		// Instrumentation is additive; no shipped sandbox/restart policy is replaced.
		for _, property := range []string{
			"BindReadOnlyPaths=" + install,
			"ExecStartPre=/bin/sh -c 'printf runtime-fixture > " + runtime + "/fixture'",
			"ExecStopPost=/bin/sh -c 'exec /usr/bin/systemctl show " + unit + " --property=ExecMainCode --property=ExecMainStatus --property=Result > " + exitPath + "'",
		} {
			args = append(args, "--property="+property)
		}
		run("systemd-run", append(args, binary, "-c", configPath)...)
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
			if time.Now().After(deadline) {
				t.Fatalf("systemd application readiness timed out: %s", run("systemctl", "show", unit, "--property=ActiveState", "--property=ExecMainStatus"))
			}
			time.Sleep(20 * time.Millisecond)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(run("systemctl", "show", unit, "--property=MainPID", "--value")))
		if err != nil || pid <= 0 {
			t.Fatalf("invalid MainPID=%d: %v", pid, err)
		}
		assertNativeSystemdProcess(t, pid, map[string]string{root: "ro", state: "rw", runtime: "rw", configDir: "ro", configPath: "rw", install: "ro"})
		policy := run("systemctl", "show", unit, "--property=TimeoutStopUSec", "--property=KillMode", "--property=LimitNOFILE", "--property=UMask")
		for _, want := range []string{"TimeoutStopUSec=30s\n", "KillMode=control-group\n", "LimitNOFILE=65536\n", "UMask=0077\n"} {
			if !strings.Contains(policy, want) {
				t.Fatalf("deployment service effective policy missing %q: %s", want, policy)
			}
		}
		environment, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			t.Fatal(err)
		}
		marker, err := os.ReadFile(filepath.Join(runtime, "fixture"))
		if err != nil || string(marker) != "runtime-fixture" {
			t.Fatalf("service could not write its managed runtime directory: %q %v", marker, err)
		}
		resp, err := client.Get(base + "/System/Info/Public")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			ID string `json:"Id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || body.ID == "" || (iteration > 0 && body.ID != serverID) {
			t.Fatalf("public identity after restart: %q -> %q status=%d %v", serverID, body.ID, resp.StatusCode, err)
		}
		serverID = body.ID
		if iteration == 0 {
			pid = assertNativeSystemdFailureRestart(t, run, unit, pid, base, serverID, exitPath)
			assertNativeSystemdProcess(t, pid, map[string]string{root: "ro", state: "rw", runtime: "rw", configDir: "ro", configPath: "rw", install: "ro"})
		}
		var assertProbeStopped func()
		if iteration == 2 {
			assertProbeStopped = admitNativeSystemdProbe(t, base, state, runtime, pid)
		}
		if iteration == 3 {
			servicePATH := ""
			for _, entry := range strings.Split(string(environment), "\x00") {
				if value, ok := strings.CutPrefix(entry, "PATH="); ok {
					servicePATH = value
				}
			}
			if servicePATH != "/usr/local/bin:/usr/bin:/bin" {
				t.Fatalf("unexpected deployment PATH=%q", servicePATH)
			}
			resolved := ""
			for _, dir := range filepath.SplitList(servicePATH) {
				path := filepath.Join(dir, "ffprobe")
				info, err := os.Stat(path)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
					resolved, err = filepath.EvalSymlinks(path)
					if err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if resolved != realProbePath {
				t.Fatalf("service PATH selects %q, opt-in requested %q", resolved, realProbePath)
			}
			data, err := os.ReadFile(filepath.Join(fmt.Sprintf("/proc/%d/root", pid), resolved))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("real ffprobe via service PATH=%s executable=%s SHA256=%x", servicePATH, resolved, sha256.Sum256(data))
			assertNativeSystemdRealProbe(t, base, state)
			assertNativeSystemdReadBoundaries(t, base, state)
		}
		if iteration == 1 {
			// Web 自升级路径：程序收到「升级完成」后以 0 正常退出，Restart=always
			// 必须立即拉起新进程；这不是失败重启。
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(15 * time.Second)
			for {
				next, err := strconv.Atoi(strings.TrimSpace(run("systemctl", "show", unit, "--property=MainPID", "--value")))
				if err == nil && next > 0 && next != pid {
					pid = next
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("successful application exit was not restarted by Restart=always")
				}
				time.Sleep(20 * time.Millisecond)
			}
			for {
				resp, err := client.Get(base + "/healthz")
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("restarted process did not become ready")
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Logf("upgrade path: clean exit restarted as MainPID=%d", pid)
		}
		started := time.Now()
		run("systemctl", "stop", unit)
		if elapsed := time.Since(started); elapsed > 17*time.Second {
			t.Fatalf("systemd stop exceeded app shutdown budget plus slack: %s", elapsed)
		}
		exit, err := os.ReadFile(exitPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Result=success\n", "ExecMainCode=1\n", "ExecMainStatus=0\n"} {
			if !strings.Contains(string(exit), want) {
				t.Fatalf("not a normal systemd exit: %s", exit)
			}
		}
		t.Logf("run %d normal systemd stop in %s: %s", iteration, time.Since(started), exit)
		if iteration == 2 {
			assertProbeStopped()
		}
		for _, path := range []string{configPath, dbPath} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			owner := info.Sys().(*syscall.Stat_t)
			if owner.Uid != 65534 || owner.Gid != 65534 || info.Mode().Perm() != 0o600 {
				t.Fatalf("service file %s uid=%d gid=%d mode=%v", path, owner.Uid, owner.Gid, info.Mode())
			}
		}
		persisted, err := os.ReadFile(configPath)
		if err != nil || !strings.Contains(string(persisted), serverID) {
			t.Fatalf("config server ID not persisted through read-write file bind: %v %s", err, persisted)
		}
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 {
			err = db.SetSetting("systemd_restart_fixture", "retained")
		} else if got, e := db.Setting("systemd_restart_fixture"); e != nil || got != "retained" {
			t.Errorf("setting after real unit restart=%q %v", got, e)
		}
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(configDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Fatalf("unexpected config parent contents: %v %v", entries, err)
	}
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatal(err)
	}
	owner := info.Sys().(*syscall.Stat_t)
	if owner.Uid != 0 || owner.Gid != 65534 || info.Mode().Perm() != 0o750 {
		t.Fatalf("config parent ownership/permissions changed: uid=%d gid=%d mode=%v", owner.Uid, owner.Gid, info.Mode())
	}
}
