package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCLIExtraBackdrops(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "embybackup")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	lib := filepath.Join(root, "library")
	if err := os.MkdirAll(filepath.Join(lib, "extrafanart"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "a.nfo"), []byte("<movie/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "extrafanart", "fanart1.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "db.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE libraries(id INTEGER PRIMARY KEY,path TEXT); INSERT INTO libraries VALUES(1,?)", lib); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		counts string
	}{
		{"defaults", nil, "NFO 1 个，额外背景图 1 个"},
		{"no-nfo", []string{"--nfo=false"}, "NFO 0 个，额外背景图 1 个"},
		{"no-extra", []string{"--extra-backdrops=false"}, "NFO 1 个，额外背景图 0 个"},
		{"db-only", []string{"--nfo=false", "--extra-backdrops=false"}, "NFO 0 个，额外背景图 0 个"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outDir := filepath.Join(root, tc.name)
			args := append([]string{"--db", dbPath, "--out", outDir}, tc.args...)
			out, err := exec.Command(bin, args...).CombinedOutput()
			if err != nil || !strings.Contains(string(out), tc.counts) {
				t.Fatalf("CLI: %v\n%s", err, out)
			}
			entries, err := os.ReadDir(outDir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			_, err = os.Stat(filepath.Join(outDir, entries[0].Name(), "extra-backdrops", "lib-1", "extrafanart", "fanart1.jpg"))
			wantExtra := tc.name == "defaults" || tc.name == "no-nfo"
			if (wantExtra && err != nil) || (!wantExtra && !os.IsNotExist(err)) {
				t.Fatalf("extra file want=%v err=%v", wantExtra, err)
			}
		})
	}
}
