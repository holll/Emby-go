//go:build linux

package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestNativeInstallCheckArguments(t *testing.T) {
	script, err := filepath.Abs("../../deploy/verify-native-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing", nil, "usage:"},
		{"extra", []string{digest, "/", "extra"}, "usage:"},
		{"short-sha", []string{"invalid"}, "64 hexadecimal characters"},
		{"nonhex-sha", []string{strings.Repeat("z", 64)}, "64 hexadecimal characters"},
		{"relative-root", []string{digest, "relative"}, "staging root must be absolute"},
		{"missing-root", []string{digest, filepath.Join(t.TempDir(), "absent")}, "staging root is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command("sh", append([]string{script}, tc.args...)...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("err=%v output=%s want=%q", err, out, tc.want)
			}
		})
	}
}

func TestNativeInstallCheckLayout(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("installation layout fixtures need root for ownership checks")
	}
	script, err := filepath.Abs("../../deploy/verify-native-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, change, want string }{
		{"complete", "", "PASS: static installation checks only"},
		{"uppercase-sha", "uppercase-sha", "PASS: static installation checks only"},
		{"wrong-sha", "wrong-sha", "SHA256 does not match"},
		{"missing-binary", "missing-binary", "application binary is not a regular file"},
		{"not-executable", "not-executable", "application binary is not executable"},
		{"binary-mode", "binary-mode", "ownership/mode is"},
		{"binary-owner", "binary-owner", "ownership/mode is"},
		{"prefix-mode", "prefix-mode", "ownership/mode is"},
		{"config-mode", "config-mode", "ownership/mode is"},
		{"config-owner", "config-owner", "ownership/mode is"},
		{"config-dir-mode", "config-dir-mode", "ownership/mode is"},
		{"missing-config", "missing-config", "configuration is not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, text string, mode fs.FileMode, uid, gid int) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(path, uid, gid); err != nil {
					t.Fatal(err)
				}
			}
			for _, dir := range []struct {
				path     string
				mode     fs.FileMode
				uid, gid int
			}{
				{"opt/emby-go", 0755, 0, 0},
				{"etc/emby-go", 0755, 0, 0},
			} {
				path := filepath.Join(root, dir.path)
				if err := os.MkdirAll(path, dir.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, dir.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(path, dir.uid, dir.gid); err != nil {
					t.Fatal(err)
				}
			}
			for _, dir := range []string{"opt", "etc"} {
				path := filepath.Join(root, dir)
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(root, "opt/emby-go/emby-go")
			image := "#!/bin/sh\nexit 0\n"
			write(binary, image, 0755, 0, 0)
			config := filepath.Join(root, "etc/emby-go/config.yaml")
			write(config, "secret: installation-audit-canary\n", 0600, 0, 0)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(image)))
			switch tc.change {
			case "uppercase-sha":
				digest = strings.ToUpper(digest)
			case "wrong-sha":
				digest = strings.Repeat("0", 64)
			case "missing-binary":
				err = os.Remove(binary)
			case "not-executable":
				err = os.Chmod(binary, 0644)
			case "binary-mode":
				err = os.Chmod(binary, 0775)
			case "binary-owner":
				err = os.Chown(binary, 65534, 65534)
			case "prefix-mode":
				err = os.Chmod(filepath.Join(root, "opt/emby-go"), 0777)
			case "config-mode":
				err = os.Chmod(config, 0644)
			case "config-owner":
				err = os.Chown(config, 65534, 65534)
			case "config-dir-mode":
				err = os.Chmod(filepath.Join(root, "etc/emby-go"), 0770)
			case "missing-config":
				err = os.Remove(config)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := nativeInstallTree(t, root)
			out, runErr := exec.Command("sh", script, digest, root).CombinedOutput()
			wantSuccess := tc.change == "" || tc.change == "uppercase-sha"
			if (runErr == nil) != wantSuccess || !strings.Contains(string(out), tc.want) {
				t.Fatalf("err=%v output=%s want=%q success=%v", runErr, out, tc.want, wantSuccess)
			}
			if strings.Contains(string(out), "installation-audit-canary") {
				t.Fatal("configuration content leaked")
			}
			if !reflect.DeepEqual(before, nativeInstallTree(t, root)) {
				t.Fatal("audit changed fixture bytes, modes or ownership")
			}
			t.Logf("root flat layout: %s", strings.TrimSpace(string(out)))
		})
	}
}

func nativeInstallTree(t *testing.T, root string) map[string]struct {
	Data     string
	Mode     fs.FileMode
	UID, GID uint32
} {
	t.Helper()
	tree := make(map[string]struct {
		Data     string
		Mode     fs.FileMode
		UID, GID uint32
	})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if entry.Type().IsRegular() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		stat := info.Sys().(*syscall.Stat_t)
		tree[path] = struct {
			Data     string
			Mode     fs.FileMode
			UID, GID uint32
		}{string(data), info.Mode(), stat.Uid, stat.Gid}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
