//go:build linux

package backup

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opt-in because CLONE_NEWNS and tmpfs require CAP_SYS_ADMIN. Each child owns
// a private mount namespace and a 1 MiB tmpfs: never fill the runner's disk.
func TestRunDiskFullPreservesBackupsAndAllowsRetry(t *testing.T) {
	if os.Getenv("EMBY_GO_TEST_TMPFS_ENOSPC") != "1" {
		t.Skip("set EMBY_GO_TEST_TMPFS_ENOSPC=1 with mount namespace/tmpfs privileges")
	}
	if os.Getenv("EMBY_GO_TEST_ENOSPC_PARENT_NS") == "" {
		parentNS, err := os.Readlink("/proc/self/ns/mnt")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRunDiskFullPreservesBackupsAndAllowsRetry$", "-test.v", "-test.timeout=60s")
		cmd.Env = append(os.Environ(), "EMBY_GO_TEST_ENOSPC_PARENT_NS="+parentNS)
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
		output, err := cmd.CombinedOutput()
		t.Logf("isolated ENOSPC child:\n%s", output)
		if err != nil {
			t.Fatalf("isolated ENOSPC child failed: %v", err)
		}
		return
	}

	// Verify isolation before changing propagation: even an accidentally inherited
	// helper environment must not let this test mount into the parent's namespace.
	currentNS, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if currentNS == os.Getenv("EMBY_GO_TEST_ENOSPC_PARENT_NS") {
		t.Fatal("ENOSPC child did not enter a separate mount namespace")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, diagnostic string
		beforeSnapshot   bool
		nfo              bool
	}{
		{"snapshot", "backup: snapshot:", true, true},
		{"nfo", "backup: copy nfo:", false, true},
		{"extra-backdrops", "backup: copy extra backdrops:", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, lib, out := fixture(t)
			if err := os.Mkdir(out, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mount("tmpfs", out, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "size=1m,mode=0700"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := syscall.Unmount(out, 0); err != nil {
					t.Error(err)
				}
			})
			imagePath := filepath.Join(lib, "extrafanart", "fanart1.jpg")
			if err := os.MkdirAll(filepath.Dir(imagePath), 0o700); err != nil {
				t.Fatal(err)
			}
			// Backup copies canonical image files verbatim; image decoding is not
			// involved in this destination write-boundary test.
			if err := os.WriteFile(imagePath, bytes.Repeat([]byte("image"), 100), 0o600); err != nil {
				t.Fatal(err)
			}
			opts := Options{DBPath: dbPath, OutDir: out, NFORoot: tc.nfo, ExtraBackdrops: true, Now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}
			first, err := Run(opts)
			if err != nil {
				t.Fatal(err)
			}
			opts.Now = opts.Now.Add(time.Hour)
			second, err := Run(opts)
			if err != nil {
				t.Fatal(err)
			}
			updatedImage := []byte("changed before failed backup")
			if err := os.WriteFile(imagePath, updatedImage, 0o600); err != nil {
				t.Fatal(err)
			}
			oldBackups, oldLibrary := readBackupTree(t, out), readBackupTree(t, lib)
			oldSecond := readBackupTree(t, second.Dir)
			oldDB, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}

			fillPath := filepath.Join(out, ".enospc-fill")
			injected := 0
			fill := func() {
				f, err := os.Create(fillPath)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				block := make([]byte, 4096)
				// The tmpfs is 1 MiB. A 2 MiB attempted-write ceiling bounds
				// this fixture independently of whether exhaustion is observed.
				for range 512 {
					if _, err := f.Write(block); err != nil {
						if !errors.Is(err, syscall.ENOSPC) {
							t.Fatalf("fill returned %v, want ENOSPC", err)
						}
						injected++
						t.Logf("real OS ENOSPC on 1 MiB private tmpfs before %s", tc.name)
						return
					}
				}
				t.Fatal("private tmpfs did not reach ENOSPC within 2 MiB")
			}
			if tc.beforeSnapshot {
				testHookBeforeSnapshot = fill
			} else {
				testHookAfterSnapshot = fill
			}
			t.Cleanup(func() { testHookBeforeSnapshot, testHookAfterSnapshot = nil, nil })
			opts.Retain = 1
			result, runErr := Run(opts)
			testHookBeforeSnapshot, testHookAfterSnapshot = nil, nil
			if injected != 1 || runErr == nil || !strings.Contains(runErr.Error(), tc.diagnostic) || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("injected=%d result=%+v err=%v", injected, result, runErr)
			}
			if !tc.beforeSnapshot && !errors.Is(runErr, syscall.ENOSPC) {
				t.Fatalf("copy failure did not preserve OS ENOSPC: %v", runErr)
			}
			t.Logf("backup rejected exhausted destination: %v", runErr)
			// Only remove the filler we created in this fixture's mounted output.
			if err := os.Remove(fillPath); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(readBackupTree(t, out), oldBackups) || !reflect.DeepEqual(readBackupTree(t, lib), oldLibrary) {
				t.Fatal("ENOSPC changed backups/source media or left staging/lock files")
			}
			if data, err := os.ReadFile(dbPath); err != nil || !bytes.Equal(data, oldDB) {
				t.Fatalf("ENOSPC changed source database: %v", err)
			}
			retry, err := Run(opts)
			if err != nil {
				t.Fatalf("retry after freeing space: %v", err)
			}
			wantNFO := 0
			if tc.nfo {
				wantNFO = 2
			}
			if retry.Dir != second.Dir || retry.NFOCount != wantNFO || retry.ExtraBackdropCount != 1 || !reflect.DeepEqual(retry.Pruned, []string{filepath.Base(first.Dir)}) {
				t.Fatalf("retry=%+v", retry)
			}
			if err := checkSnapshot(retry.Database); err != nil {
				t.Fatal(err)
			}
			oldSecond[filepath.Join("extra-backdrops", "lib-1", "extrafanart", "fanart1.jpg")] = string(updatedImage)
			if !reflect.DeepEqual(readBackupTree(t, retry.Dir), oldSecond) {
				t.Fatal("retry did not publish the expected complete backup")
			}
			entries, err := os.ReadDir(out)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(retry.Dir) {
				t.Fatalf("retry left unexpected output entries: %v, %v", entries, err)
			}
		})
	}
}
