package backup

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsOverlappingOutputBeforeWriting(t *testing.T) {
	for _, name := range []string{"same", "nested", "ancestor", "relative", "output-symlink", "library-symlink"} {
		t.Run(name, func(t *testing.T) {
			dbPath, libDir, _ := fixture(t)
			outDir := libDir
			switch name {
			case "nested":
				outDir = filepath.Join(libDir, "new", "backups")
			case "ancestor":
				outDir = filepath.Dir(libDir)
			case "relative":
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				outDir, err = filepath.Rel(cwd, filepath.Join(libDir, "backups"))
				if err != nil {
					t.Fatal(err)
				}
			case "output-symlink":
				link := filepath.Join(filepath.Dir(libDir), "alias")
				if err := os.Symlink(libDir, link); err != nil {
					t.Skip(err)
				}
				outDir = filepath.Join(link, "new", "backups")
			case "library-symlink":
				link := filepath.Join(filepath.Dir(libDir), "alias")
				if err := os.Symlink(libDir, link); err != nil {
					t.Skip(err)
				}
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("UPDATE libraries SET path=? WHERE id=1", link)
				if closeErr := db.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					t.Fatal(err)
				}
				outDir = filepath.Join(libDir, "backups")
			}
			now := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
			// Existing staging and completed backups must not be touched on rejection.
			stage := filepath.Join(outDir, ".20261022T010000Z.tmp")
			complete := filepath.Join(outDir, "20261022T010000Z")
			if err := os.MkdirAll(stage, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(complete, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{stage, complete} {
				if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("untouched"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				_, err := Run(Options{DBPath: dbPath, OutDir: outDir, NFORoot: true, Retain: 1, Now: now})
				if err == nil || !strings.Contains(err.Error(), "overlaps") {
					t.Fatalf("expected overlap error, got %v", err)
				}
			}
			for _, dir := range []string{stage, complete} {
				b, err := os.ReadFile(filepath.Join(dir, "keep"))
				if err != nil || string(b) != "untouched" {
					t.Fatalf("changed %s: %q %v", dir, b, err)
				}
			}
			for _, rel := range []string{"a.nfo", filepath.Join("sub", "b.nfo")} {
				b, err := os.ReadFile(filepath.Join(libDir, rel))
				if err != nil || string(b) != "<movie>"+rel+"</movie>" {
					t.Fatalf("changed source %s: %q %v", rel, b, err)
				}
			}
		})
	}
}

func TestRunRejectsMissingNestedOutputWithoutCreatingDirectories(t *testing.T) {
	dbPath, libDir, _ := fixture(t)
	out := filepath.Join(libDir, "not-created", "backups")
	if _, err := Run(Options{DBPath: dbPath, OutDir: out, NFORoot: true}); err == nil {
		t.Fatal("nested output accepted")
	}
	if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
		t.Fatalf("validation wrote directory: %v", err)
	}
}

func TestRunAllowsSeparateSiblingOutputAndSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "sibling-prefix", true: "separate-symlink"}[symlink], func(t *testing.T) {
			dbPath, libDir, _ := fixture(t)
			out := libDir + "-backups"
			if symlink {
				real := filepath.Join(filepath.Dir(libDir), "real-backups")
				if err := os.Mkdir(real, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, out); err != nil {
					t.Skip(err)
				}
			}
			for i := range 2 {
				result, err := Run(Options{DBPath: dbPath, OutDir: out, NFORoot: true, Retain: 1, Now: time.Unix(int64(i), 0)})
				if err != nil || result.NFOCount != 2 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			}
		})
	}
}

func TestRunRejectsDatabaseInsideOutputWithoutNFO(t *testing.T) {
	dbPath, _, _ := fixture(t)
	out := filepath.Dir(dbPath)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{DBPath: dbPath, OutDir: out, NFORoot: false}); err == nil {
		t.Fatal("output containing database accepted")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("source database changed: %v", err)
	}
}

func TestCanonicalPathResolvesMissingSuffixAndRejectsDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	got, err := canonicalPath(filepath.Join(link, "new", "child"))
	if err != nil || got != filepath.Join(real, "new", "child") {
		t.Fatalf("canonical=%q err=%v", got, err)
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalPath(filepath.Join(dangling, "child")); err == nil {
		t.Fatal("dangling symlink accepted")
	}
}
