//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeReleaseFixtureIgnoresDeveloperBuildFlags(t *testing.T) {
	// This regression must exercise the compiler, even during same-binary runs.
	t.Setenv("EMBY_GO_NATIVE_BINARY", "")
	t.Setenv("EMBY_GO_NATIVE_SHA256", "")
	t.Setenv("EMBY_GO_NATIVE_VERSION", "")
	t.Setenv("EMBY_GO_NATIVE_COMMIT", "")
	t.Setenv("EMBY_GO_NATIVE_BUILD_TIME", "")
	root := t.TempDir()
	goenv := filepath.Join(root, "go.env")
	const persisted = "GOFLAGS=-gcflags=all=-N -tags=fixture_persisted\nGOEXPERIMENT=fixture_invalid\nGOAMD64=v3\nGOARM64=v9.0\n"
	if err := os.WriteFile(goenv, []byte(persisted), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goenv)
	t.Setenv("GOFLAGS", "-gcflags=all=-N -tags=fixture_environment")
	t.Setenv("GOEXPERIMENT", "fixture_invalid")
	t.Setenv("GOAMD64", "v3")
	t.Setenv("GOARM64", "v9.0")
	buildNativeReleaseFixture(t, filepath.Join(root, "emby-go"))
}

func TestNativeCandidateRejectsInvalidInput(t *testing.T) {
	if os.Getenv("EMBY_GO_CANDIDATE_REJECT_CHILD") == "1" {
		buildNativeReleaseFixture(t, filepath.Join(t.TempDir(), "candidate"))
		t.Fatal("invalid candidate was accepted")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	spoof := filepath.Join(root, "spoof")
	if err := os.WriteFile(spoof, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, binary, digest, version, commit, built, message string }{
		{"missing-path", "", strings.Repeat("0", 64), "", "", "", "candidate requires"},
		{"missing-digest", spoof, "", "", "", "", "candidate requires"},
		{"malformed-digest", spoof, "not-hex", "", "", "", "candidate requires"},
		{"wrong-digest", spoof, strings.Repeat("0", 64), "", "", "", "(refusing execution)"},
		{"identity-without-candidate", "", "", "candidate", "source", "time", "explicit candidate identity requires"},
		{"missing-version", spoof, strings.Repeat("0", 64), "", "source", "time", "explicit candidate identity requires"},
		{"missing-commit", spoof, strings.Repeat("0", 64), "candidate", "", "time", "explicit candidate identity requires"},
		{"missing-build-time", spoof, strings.Repeat("0", 64), "candidate", "source", "", "explicit candidate identity requires"},
		{"version-only", spoof, strings.Repeat("0", 64), "candidate", "", "", "explicit candidate identity requires"},
		{"identity-wrong-digest", spoof, strings.Repeat("0", 64), "candidate", "source", "time", "(refusing execution)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(self, "-test.run=^TestNativeCandidateRejectsInvalidInput$")
			cmd.Env = append(os.Environ(), "EMBY_GO_CANDIDATE_REJECT_CHILD=1",
				"EMBY_GO_NATIVE_BINARY="+tc.binary, "EMBY_GO_NATIVE_SHA256="+tc.digest,
				"EMBY_GO_NATIVE_VERSION="+tc.version, "EMBY_GO_NATIVE_COMMIT="+tc.commit, "EMBY_GO_NATIVE_BUILD_TIME="+tc.built)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.message) {
				t.Fatalf("candidate rejection: error=%v output=%s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("untrusted executable ran or marker unreadable: %v", err)
			}
		})
	}
}

func TestNativeCandidateRejectsWrongIdentity(t *testing.T) {
	for _, name := range []string{"EMBY_GO_NATIVE_BINARY", "EMBY_GO_NATIVE_SHA256", "EMBY_GO_NATIVE_VERSION", "EMBY_GO_NATIVE_COMMIT", "EMBY_GO_NATIVE_BUILD_TIME"} {
		t.Setenv(name, "")
	}
	binary := filepath.Join(t.TempDir(), "candidate")
	fixture := buildNativeReleaseFixture(t, binary)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "commit", "build-time"} {
		t.Run(field, func(t *testing.T) {
			version, commit, built := fixture.Version, fixture.Commit, fixture.BuildTime
			switch field {
			case "version":
				version = "wrong-version"
			case "commit":
				commit = "wrong-commit"
			case "build-time":
				built = "wrong-build-time"
			}
			cmd := exec.Command(self, "-test.run=^TestNativeCandidateRejectsInvalidInput$")
			cmd.Env = append(os.Environ(), "EMBY_GO_CANDIDATE_REJECT_CHILD=1",
				"EMBY_GO_NATIVE_BINARY="+binary, fmt.Sprintf("EMBY_GO_NATIVE_SHA256=%x", sha256.Sum256(fixture.Image)),
				"EMBY_GO_NATIVE_VERSION="+version, "EMBY_GO_NATIVE_COMMIT="+commit, "EMBY_GO_NATIVE_BUILD_TIME="+built)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "candidate version: got=") || !strings.Contains(string(out), "wrong-") {
				t.Fatalf("wrong candidate identity accepted or failed elsewhere: %v %s", err, out)
			}
		})
	}
}

type nativeReleaseFixture struct {
	Image                      []byte
	Version, Commit, BuildTime string
}

// Apply the native release profile, or verify a pinned prebuilt candidate.
// Explicit metadata is an expected identity, not source/release authentication.
func buildNativeReleaseFixture(t *testing.T, binary string) nativeReleaseFixture {
	t.Helper()
	candidate, digest := os.Getenv("EMBY_GO_NATIVE_BINARY"), os.Getenv("EMBY_GO_NATIVE_SHA256")
	fixture := nativeReleaseFixture{Version: "native-rehearsal", Commit: "fixture", BuildTime: "fixture"}
	version, commit, built := os.Getenv("EMBY_GO_NATIVE_VERSION"), os.Getenv("EMBY_GO_NATIVE_COMMIT"), os.Getenv("EMBY_GO_NATIVE_BUILD_TIME")
	if version != "" || commit != "" || built != "" {
		if candidate == "" || digest == "" || version == "" || commit == "" || built == "" {
			t.Fatal("explicit candidate identity requires binary, SHA256, version, commit and build time together")
		}
		fixture.Version, fixture.Commit, fixture.BuildTime = version, commit, built
	}
	if candidate != "" || digest != "" {
		want, err := hex.DecodeString(digest)
		if candidate == "" || err != nil || len(want) != sha256.Size {
			t.Fatal("candidate requires EMBY_GO_NATIVE_BINARY and a 64-hex EMBY_GO_NATIVE_SHA256")
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if !bytes.Equal(sum[:], want) {
			t.Fatalf("candidate SHA256=%x want=%x (refusing execution)", sum, want)
		}
		// Copy verified bytes, not a second read of the external path. Each
		// existing fixture retains its own install/cleanup directory.
		if err := os.WriteFile(binary, data, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Logf("reusing pinned candidate SHA256=%x; not rebuilding this scenario", sum)
	} else {
		const flags = "-s -w -X main.version=native-rehearsal -X main.commit=fixture -X main.buildTime=fixture"
		build := exec.Command("go", "build", "-trimpath", "-ldflags", flags, "-o", binary, ".")
		// Ignore developer flags, including go env -w settings. Match the workflow's
		// default architecture levels rather than inheriting this host's tuning.
		build.Env = append(os.Environ(), "GOENV=off", "GOFLAGS=", "GOEXPERIMENT=",
			"CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOAMD64=v1", "GOARM64=v8.0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("release-profile fixture build: %v\n%s", err, out)
		}
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	wants := map[string]string{
		"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": runtime.GOARCH,
		"-trimpath": "true", "-buildmode": "exe",
		"-gcflags": "", "-asmflags": "", "-tags": "", "GOEXPERIMENT": "",
	}
	switch runtime.GOARCH {
	case "amd64":
		wants["GOAMD64"] = "v1"
	case "arm64":
		wants["GOARM64"] = "v8.0"
	}
	for name, want := range wants {
		if got := settings[name]; got != want {
			t.Fatalf("fixture build setting %s=%q, want %q", name, got, want)
		}
	}
	// Trimpath builds omit the ldflags setting. Verify stripping and injected
	// metadata on the actual ELF/version output instead of expecting that key.
	image, err := elf.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	for _, program := range image.Progs {
		if program.Type == elf.PT_INTERP {
			t.Fatal("release-profile fixture unexpectedly requires an ELF interpreter")
		}
	}
	libraries, err := image.ImportedLibraries()
	if err != nil || len(libraries) != 0 {
		t.Fatalf("release-profile fixture imports shared libraries: %v %v", libraries, err)
	}
	if image.Section(".symtab") != nil || image.Section(".debug_info") != nil {
		t.Fatal("release-profile fixture retained symbol/DWARF sections despite -s -w")
	}
	out, err := exec.Command(binary, "-version").CombinedOutput()
	wantVersion := fmt.Sprintf("Emby-go %s (commit %s, built %s)\n", fixture.Version, fixture.Commit, fixture.BuildTime)
	if err != nil || string(out) != wantVersion {
		t.Fatalf("candidate version: got=%q want=%q error=%v", out, wantVersion, err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("release-profile fixture SHA256=%x Go=%s linux/%s CGO=0 trimpath stripped static ELF; not a release artifact", sha256.Sum256(data), info.GoVersion, runtime.GOARCH)
	fixture.Image = data
	return fixture
}
