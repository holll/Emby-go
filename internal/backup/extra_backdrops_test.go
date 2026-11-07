package backup

import (
	"bytes"
	"database/sql"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunExtraBackdropsOptionsAndRestore(t *testing.T) {
	dbPath, lib, out := fixture(t) // Legacy schema has no extra_backdrops column.
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 24, 16)), nil); err != nil {
		t.Fatal(err)
	}
	valid := []string{"extrafanart/fanart1.jpg", "sub/extrafanart/fanart2.jpg", "sub/extrafanart/fanart10.jpg"}
	invalid := []string{"fanart1.jpg", "extrafanart/fanart0.jpg", "extrafanart/fanart01.jpg", "extrafanart/fanart+1.jpg", "extrafanart/fanart-1.jpg", "extrafanart/fanart99999999999999999999999.jpg", "extrafanart/fanart1.JPG", "extrafanart/Fanart1.jpg", "extrafanart/fanart1.png", "Extrafanart/fanart1.jpg", "extrafanart/sub/fanart1.jpg"}
	for _, rel := range append(append([]string{}, valid...), invalid...) {
		path := filepath.Join(lib, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(lib, "extrafanart", "fanart3.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}
	var run int64
	for _, nfo := range []bool{false, true} {
		for _, extra := range []bool{false, true} {
			run++
			wantNFO, wantExtra := 0, 0
			if nfo {
				wantNFO = 2
			}
			if extra {
				wantExtra = len(valid)
			}
			restoreRoot := t.TempDir()
			result, err := Run(Options{DBPath: dbPath, OutDir: out, NFORoot: nfo, ExtraBackdrops: extra, Now: time.Unix(run, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if result.NFOCount != wantNFO || result.ExtraBackdropCount != wantExtra {
				t.Fatalf("nfo=%v extra=%v result=%+v", nfo, extra, result)
			}
			var copied []string
			if extra {
				root := filepath.Join(result.Dir, "extra-backdrops", "lib-1")
				err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if info.IsDir() {
						return nil
					}
					rel, err := filepath.Rel(root, path)
					if err != nil {
						return err
					}
					copied = append(copied, filepath.ToSlash(rel))
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if !bytes.Equal(raw, encoded.Bytes()) {
						t.Errorf("changed bytes: %s", rel)
					}
					// Exercise the documented manual restore into a different root.
					restored := filepath.Join(restoreRoot, rel)
					if err := os.MkdirAll(filepath.Dir(restored), 0o755); err != nil {
						return err
					}
					if err := copyFile(path, restored); err != nil {
						return err
					}
					file, err := os.Open(restored)
					if err != nil {
						return err
					}
					defer file.Close()
					cfg, err := jpeg.DecodeConfig(file)
					if err != nil {
						return err
					}
					if cfg.Width != 24 || cfg.Height != 16 {
						t.Errorf("restored config=%+v", cfg)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				want := []string{valid[0], valid[2], valid[1]} // Filesystem traversal is lexical, not DB image order.
				if !reflect.DeepEqual(copied, want) {
					t.Fatalf("copied=%v want=%v", copied, want)
				}
			} else if _, err := os.Stat(filepath.Join(result.Dir, "extra-backdrops")); !os.IsNotExist(err) {
				t.Fatalf("disabled assets exist: %v", err)
			}
		}
	}
}

func TestExtraBackdropBackupSkipsLinksAndOfflineLibraries(t *testing.T) {
	dbPath, lib, out := fixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "fanart1.jpg"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lib, "extrafanart"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, target := range map[string]string{
		filepath.Join(lib, "extrafanart", "fanart1.jpg"): filepath.Join(outside, "fanart1.jpg"),
		filepath.Join(lib, "extrafanart", "fanart2.jpg"): filepath.Join(outside, "missing"),
		filepath.Join(lib, "sub", "extrafanart"):         outside,
		filepath.Join(lib, "linked-movie"):               outside,
	} {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	linkedRoot := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(outside, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO libraries VALUES(2,?),(3,?)", linkedRoot, filepath.Join(outside, "offline")); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Options{DBPath: dbPath, OutDir: out, ExtraBackdrops: true})
	if err != nil || result.ExtraBackdropCount != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExtraBackdropCopyFailurePreservesPublishedBackup(t *testing.T) {
	dbPath, lib, out := fixture(t)
	path := filepath.Join(lib, "extrafanart", "fanart1.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{DBPath: dbPath, OutDir: out, ExtraBackdrops: true, Now: time.Unix(100, 0), Retain: 1}
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	testHookAfterSnapshot = func() {
		stages, err := filepath.Glob(filepath.Join(out, ".*.tmp"))
		if err != nil || len(stages) != 1 {
			t.Fatalf("stages=%v err=%v", stages, err)
		}
		if err := os.WriteFile(filepath.Join(stages[0], "extra-backdrops"), []byte("block mkdir"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { testHookAfterSnapshot = nil }()
	if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "copy extra backdrops") {
		t.Fatalf("expected copy failure: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(first.Dir, "extra-backdrops", "lib-1", "extrafanart", "fanart1.jpg"))
	if err != nil || string(stored) != "original" {
		t.Fatalf("previous backup changed: %q %v", stored, err)
	}
	if err := checkSnapshot(first.Database); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(first.Dir) {
		t.Fatalf("failed stage/lock retained: %v", entries)
	}
}

func TestExtraBackdropBackupRequiresLibrariesAndProtectsOverlap(t *testing.T) {
	dbPath, lib, out := fixture(t)
	if _, err := Run(Options{DBPath: dbPath, OutDir: filepath.Join(lib, "backup"), ExtraBackdrops: true}); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap accepted: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE libraries"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Run(Options{DBPath: dbPath, OutDir: out, ExtraBackdrops: true}); err == nil {
		t.Fatal("extra backup silently accepted missing library schema")
	}
	if _, err := Run(Options{DBPath: dbPath, OutDir: out}); err != nil {
		t.Fatalf("DB-only compatibility: %v", err)
	}
}
