//go:build unix

package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This subprocess is the actual executable behind the fake ffprobe launcher.
// No production process-launch hooks or test-only budgets are needed.
func TestProbeResourceProcess(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--probe-helper" {
			index = i + 1
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index:]
	switch args[0] {
	case "emit":
		out, _ := strconv.Atoi(args[1])
		errSize, _ := strconv.Atoi(args[2])
		text := `{"streams":[{"codec_type":"video","codec_name":"h264"}]}`
		if args[len(args)-1] == "-version" {
			text = "ffprobe version fixture"
		}
		_, _ = os.Stdout.Write([]byte(text))
		if out > len(text) {
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{' '}, out-len(text)))
		}
		_, _ = os.Stderr.Write(bytes.Repeat([]byte{'x'}, errSize))
		if out > MaxStdoutSize || errSize > MaxStderrSize {
			time.Sleep(30 * time.Second) // overflow must terminate, not await exit
		}
	case "hold":
		_ = os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600)
		for {
			time.Sleep(time.Second)
		}
	case "tree", "tree-exit", "tree-overflow":
		child := exec.Command(os.Args[0], "-test.run=^TestProbeResourceProcess$", "--", "--probe-helper", "hold", args[1])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		for {
			if _, err := os.Stat(args[1]); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if args[0] == "tree-overflow" {
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, MaxStdoutSize+1))
		}
		if args[0] != "tree-exit" {
			_ = child.Wait()
		}
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func probeLauncher(t *testing.T, args ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Race-instrumented helper children need no one-second shutdown sleep.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nexec " + quote(exe) + " '-test.run=^TestProbeResourceProcess$' -- --probe-helper"
	for _, arg := range args {
		script += " " + quote(arg)
	}
	script += " \"$@\"\n"
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func awaitProbeMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("probe process did not start")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProbeOutputLimits(t *testing.T) {
	for _, verify := range []bool{false, true} {
		for _, tc := range []struct {
			name        string
			out, stderr int
			want        error
		}{
			{"exact", MaxStdoutSize, MaxStderrSize, nil},
			{"stdout_over", MaxStdoutSize + 1, 0, ErrStdoutTooLarge},
			{"stderr_over", 128, MaxStderrSize + 1, ErrStderrTooLarge},
		} {
			t.Run(fmt.Sprintf("verify_%t/%s", verify, tc.name), func(t *testing.T) {
				path := probeLauncher(t, "emit", strconv.Itoa(tc.out), strconv.Itoa(tc.stderr))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				start := time.Now()
				var err error
				if verify {
					err = Verify(ctx, path)
				} else {
					var result Result
					result, err = Run(ctx, path, "fixture", time.Second*4)
					if tc.want == nil && (len(result.Raw) != tc.out || result.Info.Video == nil) {
						t.Fatalf("successful output not retained/parsed: %d", len(result.Raw))
					}
					if tc.want != nil && result.Raw != nil {
						t.Fatal("failure returned Raw")
					}
				}
				if !errors.Is(err, tc.want) {
					t.Fatalf("error %v, want %v", err, tc.want)
				}
				if tc.want != nil && errors.Is(err, context.Canceled) {
					t.Fatalf("overflow mistaken for manual cancellation: %v", err)
				}
				if time.Since(start) > 3*time.Second {
					t.Fatal("overflow did not terminate process promptly")
				}
				if tc.want != nil {
					good := probeLauncher(t, "emit", "128", "0")
					if _, err := Run(context.Background(), good, "fixture", time.Second); err != nil {
						t.Fatalf("healthy Run after overflow: %v", err)
					}
					if err := Verify(context.Background(), good); err != nil {
						t.Fatalf("healthy Verify after overflow: %v", err)
					}
				}
			})
		}
	}
}

func TestProbeSharedAdmissionAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := range 2 {
		marker := filepath.Join(t.TempDir(), "started")
		path := probeLauncher(t, "hold", marker)
		if i == 0 {
			go func() { _, err := Run(ctx, path, "fixture", 5*time.Second); done <- err }()
		} else {
			go func() { done <- Verify(ctx, path) }()
		}
		awaitProbeMarker(t, marker)
	}
	if len(processSlots) != 2 {
		t.Fatalf("shared slots in use: %d", len(processSlots))
	}
	marker := filepath.Join(t.TempDir(), "must-not-start")
	path := probeLauncher(t, "hold", marker)
	for _, verify := range []bool{false, true} {
		waitCtx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if verify {
			if err := Verify(waitCtx, path); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Verify admission deadline: %v", err)
			}
		} else {
			result, err := Run(waitCtx, path, "fixture", time.Minute)
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || result.Raw != nil {
				t.Errorf("Run admission deadline: %v", err)
			}
		}
		stop()
	}
	if _, err := Run(context.Background(), path, "fixture", 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run's own budget must include admission: %v", err)
	}
	waitCtx, stop := context.WithCancel(context.Background())
	stop()
	if _, err := Run(waitCtx, path, "fixture", time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("queued manual cancellation: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("queued probe spawned: %v", err)
	}
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("active cancellation: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("active probes did not release")
		}
	}
	if len(processSlots) != 0 {
		t.Fatal("admission slot leaked")
	}
	good := probeLauncher(t, "emit", "128", "0")
	if _, err := Run(context.Background(), good, "fixture", time.Second); err != nil {
		t.Fatalf("slot not reusable: %v", err)
	}
}

func TestProbeBudgetAndFailureRelease(t *testing.T) {
	// Exercise real shorter caller deadlines without waiting out the 60s cap.
	for _, timeout := range []time.Duration{-time.Second, 0, MaxTimeout, 2 * MaxTimeout} {
		marker := filepath.Join(t.TempDir(), "started")
		path := probeLauncher(t, "hold", marker)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		result, err := Run(ctx, path, "fixture", timeout)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || result.Raw != nil {
			t.Fatalf("timeout=%v: %v", timeout, err)
		}
		if len(processSlots) != 0 {
			t.Fatal("deadline leaked admission slot")
		}
	}
	path := probeLauncher(t, "hold", filepath.Join(t.TempDir(), "started"))
	result, err := Run(context.Background(), path, "fixture", 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || result.Raw != nil {
		t.Fatalf("short Run budget: %v", err)
	}
	for range 3 {
		if _, err := Run(context.Background(), "/missing-ffprobe-fixture", "fixture", time.Second); err == nil {
			t.Fatal("missing executable accepted")
		}
		if err := Verify(context.Background(), "/missing-ffprobe-fixture"); err == nil {
			t.Fatal("missing executable verified")
		}
	}
	if len(processSlots) != 0 {
		t.Fatal("start failure leaked admission slot")
	}
	good := probeLauncher(t, "emit", "128", "0")
	if err := Verify(context.Background(), good); err != nil {
		t.Fatalf("Verify after failures: %v", err)
	}
}
