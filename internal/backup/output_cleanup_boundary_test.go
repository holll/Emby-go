package backup

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setBackupLibraryPath(t *testing.T, dbPath, lib string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE libraries SET path=? WHERE id=1", lib)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunProtectsLibrariesWithoutNFOCopy(t *testing.T) {
	for _, name := range []string{"replace", "prune"} {
		t.Run(name, func(t *testing.T) {
			dbPath, _, _ := fixture(t)
			out := filepath.Join(t.TempDir(), "backups")
			stamp := "20000101T000000Z"
			if name == "replace" {
				stamp = "20261022T010000Z"
			}
			lib := filepath.Join(out, stamp)
			if err := os.MkdirAll(lib, 0o755); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(lib, "keep.nfo")
			if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			setBackupLibraryPath(t, dbPath, lib)
			_, err := Run(Options{DBPath: dbPath, OutDir: out, NFORoot: false, Retain: 1, Now: time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)})
			if err == nil || !strings.Contains(err.Error(), "overlaps") {
				t.Fatalf("expected overlap rejection, got %v", err)
			}
			if b, err := os.ReadFile(source); err != nil || string(b) != "source" {
				t.Fatalf("source changed: %q %v", b, err)
			}
		})
	}
}

func TestRunResolvesSymlinkBeforeParentTraversal(t *testing.T) {
	dbPath, _, _ := fixture(t)
	root := t.TempDir()
	child := filepath.Join(root, "source", "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(child, alias); err != nil {
		t.Skip(err)
	}
	// Raw spelling is intentional: filepath.Join would prematurely erase alias/...
	out := alias + string(filepath.Separator) + ".." + string(filepath.Separator) + "backups"
	lib := filepath.Join(root, "source", "backups", "20000101T000000Z")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(lib, "keep.nfo")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	setBackupLibraryPath(t, dbPath, lib)
	for _, copyNFO := range []bool{false, true} {
		if _, err := Run(Options{DBPath: dbPath, OutDir: out, NFORoot: copyNFO, Retain: 1}); err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("NFO=%v expected overlap: %v", copyNFO, err)
		}
	}
	if b, err := os.ReadFile(source); err != nil || string(b) != "source" {
		t.Fatalf("source changed: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
		t.Fatalf("lexically cleaned output was created: %v", err)
	}
	for _, path := range []string{out, out + string(filepath.Separator) + "new"} {
		got, err := canonicalPath(path)
		want := filepath.Join(root, "source", "backups")
		if strings.HasSuffix(path, string(filepath.Separator)+"new") {
			want = filepath.Join(want, "new")
		}
		if err != nil || got != want {
			t.Fatalf("canonical=%q want=%q err=%v", got, want, err)
		}
	}
}
