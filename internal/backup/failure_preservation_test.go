package backup

import (
	"bytes"
	"image"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise real filesystem errors after admission, without permissions tests
// that silently succeed under root or a new production fault-injection hook.
func TestRunStageFailurePreservesBackupsAndAllowsRetry(t *testing.T) {
	for _, tc := range []struct {
		name, conflict, diagnostic string
		beforeSnapshot             bool
	}{
		{"snapshot", "db.sqlite", "backup: snapshot:", true},
		{"nfo", "nfo", "backup: copy nfo:", false},
		{"extra-backdrops", "extra-backdrops", "backup: copy extra backdrops:", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, lib, out := fixture(t)
			var jpegData bytes.Buffer
			if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(lib, "extrafanart", "fanart1.jpg")
			if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(imagePath, jpegData.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			base := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
			opts := Options{DBPath: dbPath, OutDir: out, NFORoot: true, ExtraBackdrops: true, Now: base}
			first, err := Run(opts)
			if err != nil {
				t.Fatal(err)
			}
			opts.Now = base.Add(time.Hour)
			second, err := Run(opts)
			if err != nil {
				t.Fatal(err)
			}

			// Make the next backup observably different from the published one.
			updatedNFO := []byte("<movie><title>updated before backup</title></movie>")
			if err := os.WriteFile(filepath.Join(lib, "a.nfo"), updatedNFO, 0o600); err != nil {
				t.Fatal(err)
			}

			// Include directory entries as well as bytes: leftover staging/lock
			// files, removal and replacement of old backups must all be visible.
			oldBackups, oldLibrary := readBackupTree(t, out), readBackupTree(t, lib)
			oldSecond := readBackupTree(t, second.Dir)
			oldDB, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			injected := 0
			inject := func() {
				entries, err := os.ReadDir(out)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
						continue
					}
					path := filepath.Join(out, entry.Name(), tc.conflict)
					if tc.beforeSnapshot {
						err = os.Mkdir(path, 0o755)
					} else {
						err = os.WriteFile(path, []byte("destination path conflict"), 0o600)
					}
					if err != nil {
						t.Fatal(err)
					}
					injected++
				}
			}
			if tc.beforeSnapshot {
				testHookBeforeSnapshot = inject
			} else {
				testHookAfterSnapshot = inject
			}
			t.Cleanup(func() { testHookBeforeSnapshot, testHookAfterSnapshot = nil, nil })
			// Same timestamp tests replacement preservation; retain=1 would also
			// remove the older backup if pruning ran before successful publish.
			opts.Retain = 1
			result, runErr := Run(opts)
			testHookBeforeSnapshot, testHookAfterSnapshot = nil, nil
			if injected != 1 || runErr == nil || !strings.Contains(runErr.Error(), tc.diagnostic) {
				t.Fatalf("injected=%d result=%+v err=%v", injected, result, runErr)
			}
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failed run returned published result: %+v", result)
			}
			if !reflect.DeepEqual(readBackupTree(t, out), oldBackups) {
				t.Fatal("failed run changed existing backups or left staging/lock files")
			}
			if !reflect.DeepEqual(readBackupTree(t, lib), oldLibrary) {
				t.Fatal("failed run changed source media")
			}
			if data, err := os.ReadFile(dbPath); err != nil || !bytes.Equal(data, oldDB) {
				t.Fatalf("failed run changed source database: %v", err)
			}

			retry, err := Run(opts)
			if err != nil {
				t.Fatalf("retry after filesystem failure: %v", err)
			}
			if retry.Dir != second.Dir || retry.NFOCount != 2 || retry.ExtraBackdropCount != 1 || !reflect.DeepEqual(retry.Pruned, []string{filepath.Base(first.Dir)}) {
				t.Fatalf("retry=%+v", retry)
			}
			if err := checkSnapshot(retry.Database); err != nil {
				t.Fatal(err)
			}
			oldSecond[filepath.Join("nfo", "lib-1", "a.nfo")] = string(updatedNFO)
			if !reflect.DeepEqual(readBackupTree(t, retry.Dir), oldSecond) {
				t.Fatal("retry backup tree mismatch")
			}
			entries, err := os.ReadDir(out)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(retry.Dir) {
				t.Fatalf("retry left unexpected output entries: %v, %v", entries, err)
			}
		})
	}
}

// readBackupTree captures directory entries and bytes, including unexpected
// staging/lock artifacts, for both path-conflict and real ENOSPC regressions.
func readBackupTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[rel+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
