package backup

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func fixture(t *testing.T) (dbPath, libDir, outDir string) {
	t.Helper()
	root := t.TempDir()
	dbPath = filepath.Join(root, "emby.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	libDir = filepath.Join(root, "library")
	if err := os.MkdirAll(filepath.Join(libDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE libraries(id INTEGER PRIMARY KEY, path TEXT);
INSERT INTO libraries VALUES(1, ?);
CREATE TABLE movies(id INTEGER PRIMARY KEY, title TEXT);
INSERT INTO movies VALUES(1,'A');`, libDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.nfo", filepath.Join("sub", "b.nfo"), "c.strm"} {
		if err := os.WriteFile(filepath.Join(libDir, name), []byte("<movie>"+name+"</movie>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	outDir = filepath.Join(root, "backups")
	return dbPath, libDir, outDir
}

func TestRunSnapshotsDatabaseAndNFO(t *testing.T) {
	dbPath, _, outDir := fixture(t)
	now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	result, err := Run(Options{DBPath: dbPath, OutDir: outDir, Retain: 7, Now: now, NFORoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(result.Dir) != "20261018T010000Z" || result.NFOCount != 2 {
		t.Fatalf("result=%+v", result)
	}
	db, err := sql.Open("sqlite", result.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow("SELECT title FROM movies WHERE id=1").Scan(&title); err != nil || title != "A" {
		t.Fatalf("backup db title=%q err=%v", title, err)
	}
	for _, rel := range []string{"lib-1/a.nfo", filepath.Join("lib-1", "sub", "b.nfo")} {
		if _, err := os.Stat(filepath.Join(result.Dir, "nfo", rel)); err != nil {
			t.Fatalf("missing nfo %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(result.Dir, "nfo", "lib-1", "c.strm")); !os.IsNotExist(err) {
		t.Fatalf("strm should not be copied: %v", err)
	}
}

func TestRunPrunesOnlyTimestampedBackups(t *testing.T) {
	dbPath, _, outDir := fixture(t)
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := Run(Options{DBPath: dbPath, OutDir: outDir, Retain: 7, NFORoot: true, Now: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(outDir, "manual-keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Options{DBPath: dbPath, OutDir: outDir, Retain: 2, NFORoot: true, Now: base.Add(5 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pruned) != 3 {
		t.Fatalf("pruned=%v", result.Pruned)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 3 { // 最新 2 份 + 非时间戳目录
		t.Fatalf("dirs=%v", dirs)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manual-keep")); err != nil {
		t.Fatal("non-timestamped dir was pruned")
	}
}

func TestRunRequiresPathsAndSkipsOfflineLibraries(t *testing.T) {
	if _, err := Run(Options{}); err == nil {
		t.Fatal("empty options accepted")
	}
	dbPath, libDir, outDir := fixture(t)
	if err := os.RemoveAll(libDir); err != nil {
		t.Fatal(err)
	}
	result, err := Run(Options{DBPath: dbPath, OutDir: outDir, Retain: 7, NFORoot: true})
	if err != nil {
		t.Fatalf("offline library should not fail backup: %v", err)
	}
	if result.NFOCount != 0 {
		t.Fatalf("nfo count=%d", result.NFOCount)
	}
	if _, err := os.Stat(result.Database); err != nil {
		t.Fatal(err)
	}
}
