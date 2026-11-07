//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"emby-go/internal/store"
)

// This builds and runs the real entry point, not a Handler or a re-executed
// test helper. It is a local Linux smoke test, not a systemd/release certificate.
func TestNativeBinaryRestart(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "emby-go")
	fixture := buildNativeReleaseFixture(t, binary)
	t.Run("current-user", func(t *testing.T) {
		nativeBinaryRestart(t, binary, root, root, nil, fixture)
	})
	t.Run("service-account", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("requires root to launch a distinct numeric service UID/GID; current-user smoke remains enabled")
		}
		// Do not create accounts or change system directories. Unlike t.TempDir's
		// private parent, this installation root is traversable by the child UID.
		install, err := os.MkdirTemp("", "emby-native-account-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(install) })
		if err := os.Chmod(install, 0o755); err != nil {
			t.Fatal(err)
		}
		serviceBinary := filepath.Join(install, "emby-go")
		if err := os.WriteFile(serviceBinary, fixture.Image, 0o755); err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(install, "state")
		if err := os.Mkdir(state, 0o750); err != nil {
			t.Fatal(err)
		}
		credential := &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{65534}}
		if err := os.Chown(state, int(credential.Uid), int(credential.Gid)); err != nil {
			t.Fatal(err)
		}
		// Model /etc/emby-go separately from StateDirectory: only the
		// configuration file belongs to the service, not its parent directory.
		configDir := filepath.Join(install, "etc", "emby-go")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Match the install example: root:service-group 0750, with traversal
		// granted through the service group but no directory write permission.
		if err := os.Chmod(filepath.Dir(configDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(configDir, 0, int(credential.Gid)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(configDir, 0o750); err != nil {
			t.Fatal(err)
		}
		nativeBinaryRestart(t, serviceBinary, state, configDir, credential, fixture)
		entries, err := os.ReadDir(configDir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "config.yaml" {
			t.Fatalf("unexpected writes in root-owned configuration directory: %v %v", entries, err)
		}
		info, err := os.Stat(configDir)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if owner.Uid != 0 || owner.Gid != credential.Gid || info.Mode().Perm() != 0o750 {
			t.Fatalf("configuration directory ownership/permissions changed: %v", info)
		}
	})
}

// Reuse the same real-binary restart assertions for the developer identity and
// a non-root service identity; no production launcher or lifecycle is mocked.
func nativeBinaryRestart(t *testing.T, binary, root, configDir string, credential *syscall.Credential, fixture nativeReleaseFixture) {
	t.Helper()
	// Reserve a free loopback port only for fixture discovery. Production still
	// binds all interfaces: the temporary process is not a replacement GUI.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("port: %d\ndb_path: %q\nredis_addr: ''\nsetup_code: native-fixture\n", port, filepath.Join(root, "data.db"))), 0600); err != nil {
		t.Fatal(err)
	}
	if credential != nil {
		if err := os.Chown(configPath, int(credential.Uid), int(credential.Gid)); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{Timeout: time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var serverID string
	var playback nativeRestartPlayback
	for run := range 2 {
		var logs bytes.Buffer
		cmd := exec.Command(binary, "-c", configPath)
		if credential != nil {
			// The unit sets UMask=0077. A child-only shell establishes that
			// setting before exec, without changing the test runner's umask.
			cmd = exec.Command("/bin/sh", "-c", `umask 077; exec "$@"`, "native-service", binary, "-c", configPath)
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
		}
		cmd.Dir = root
		cmd.Stdout, cmd.Stderr = &logs, &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		// Always join before reading logs, including assertion failures.
		joined := false
		func() {
			defer func() {
				if !joined {
					_ = cmd.Process.Kill()
					<-done
				}
			}()
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
					t.Fatalf("startup exited: %v\n%s", err, logs.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("startup readiness timed out")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if credential != nil {
				status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
				if err != nil {
					t.Fatal(err)
				}
				for label, id := range map[string]uint32{"Uid": credential.Uid, "Gid": credential.Gid} {
					want := fmt.Sprintf("%s:\t%d\t%d\t%d\t%d\n", label, id, id, id, id)
					if !strings.Contains(string(status), want) {
						t.Fatalf("child %s did not switch to service identity: %s", label, status)
					}
				}
			}
			for path, fields := range map[string]map[string]string{
				"/api/version":        {"version": fixture.Version, "commit": fixture.Commit, "build_time": fixture.BuildTime},
				"/System/Info/Public": nil,
			} {
				resp, err := client.Get(base + path)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				err = json.NewDecoder(resp.Body).Decode(&body)
				_ = resp.Body.Close()
				if err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("%s: %d %v", path, resp.StatusCode, err)
				}
				for field, want := range fields {
					if body[field] != want {
						t.Fatalf("%s %s=%v want %s", path, field, body[field], want)
					}
				}
				if path == "/System/Info/Public" {
					id, _ := body["Id"].(string)
					if id == "" || (run > 0 && id != serverID) {
						t.Fatalf("server identity changed across restart: %q -> %q", serverID, id)
					}
					serverID = id
				}
			}
			playback.afterReady(t, base, filepath.Join(root, "data.db"), run)
			started := time.Now()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				joined = true
				if err != nil {
					t.Fatalf("shutdown: %v\n%s", err, logs.String())
				}
			case <-time.After(17 * time.Second):
				t.Fatal("SIGTERM did not finish within shutdown budget plus test slack")
			}
			if !strings.Contains(logs.String(), "已退出") || strings.Contains(logs.String(), "强制退出") {
				t.Fatalf("not graceful: %s", logs.String())
			}
			t.Logf("run %d SIGTERM joined in %s", run, time.Since(started))
		}()
		if credential != nil {
			for _, path := range []string{configPath, filepath.Join(root, "data.db")} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				owner := info.Sys().(*syscall.Stat_t)
				if owner.Uid != credential.Uid || owner.Gid != credential.Gid || info.Mode().Perm()&0o007 != 0 {
					t.Fatalf("service file ownership/other permissions: %s uid=%d gid=%d mode=%v", path, owner.Uid, owner.Gid, info.Mode())
				}
			}
			persisted, err := os.ReadFile(configPath)
			if err != nil || !strings.Contains(string(persisted), serverID) {
				t.Fatalf("service could not persist server ID to config: %v\n%s", err, persisted)
			}
		}
		// Open the actual stopped process's DB and exercise persistent state.
		db, err := store.Open(filepath.Join(root, "data.db"))
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			err = db.SetSetting("native_restart_fixture", "retained")
		} else if got, e := db.Setting("native_restart_fixture"); e != nil || got != "retained" {
			t.Errorf("persistent setting after restart=%q %v", got, e)
		}
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
