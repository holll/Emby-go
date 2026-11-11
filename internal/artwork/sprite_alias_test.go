package artwork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpriteBIFAliasPublishAndRollback(t *testing.T) {
	for _, original := range []bool{false, true} {
		for _, commit := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "absent", true: "existing"}[original], map[bool]string{false: "rollback", true: "publish"}[commit]}, "/"), func(t *testing.T) {
				dir := t.TempDir()
				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if err := root.WriteFile(".movie-generation.bif", []byte("new backing"), 0600); err != nil {
					t.Fatal(err)
				}
				if original {
					if err := root.WriteFile("movie.bif", []byte("actual old canonical"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				a, err := prepareSpriteBIFAlias(root, ".movie-generation.bif", "movie.bif")
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{a.staged, a.backup} {
					if name == "" {
						continue
					}
					info, err := root.Stat(name)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("temporary file mode: %v, %v", info, err)
					}
				}
				if err := a.commit(); err != nil {
					t.Fatal(err)
				}
				data, err := root.ReadFile("movie.bif")
				if err != nil || string(data) != "new backing" {
					t.Fatalf("published canonical %q: %v", data, err)
				}
				if err := a.finish(commit); err != nil {
					t.Fatal(err)
				}
				if err := a.finish(false); err != nil {
					t.Fatal(err)
				}
				data, err = root.ReadFile("movie.bif")
				switch {
				case commit:
					if err != nil || string(data) != "new backing" {
						t.Fatalf("committed canonical %q: %v", data, err)
					}
					if err := root.WriteFile("movie.bif", []byte("external alias edit"), 0600); err != nil {
						t.Fatal(err)
					}
					backing, err := root.ReadFile(".movie-generation.bif")
					if err != nil || string(backing) != "new backing" {
						t.Fatalf("canonical edits changed immutable backing: %q, %v", backing, err)
					}
				case original:
					if err != nil || string(data) != "actual old canonical" {
						t.Fatalf("original canonical not restored: %q, %v", data, err)
					}
				default:
					if !os.IsNotExist(err) {
						t.Fatalf("new alias survived rollback: %q, %v", data, err)
					}
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".bif-alias-") {
						t.Fatalf("temporary file leaked: %s", entry.Name())
					}
				}
			})
		}
	}
}

func TestSpriteBIFAliasRejectsUnsafePathsAndCleansPrepare(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("backing.bif", []byte("backing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("directory", 0700); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("backing.bif", "link.bif"); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][2]string{
		{"backing.bif", "backing.bif"}, {"../foreign.bif", "movie.bif"},
		{"backing.bif", "../foreign.bif"}, {filepath.Join(dir, "backing.bif"), "movie.bif"},
		{"backing.bif", "directory/movie.bif"}, {"link.bif", "movie.bif"},
		{"backing.bif", "link.bif"}, {"backing.bif", "directory"},
		{"directory", "movie.bif"}, {"missing.bif", "movie.bif"},
	} {
		if a, err := prepareSpriteBIFAlias(root, paths[0], paths[1]); err == nil {
			_ = a.finish(false)
			t.Fatalf("unsafe paths accepted: %v", paths)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("failed preparation leaked files: %v", entries)
	}
	a, err := prepareSpriteBIFAlias(root, "backing.bif", "movie.bif")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.finish(false); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("movie.bif"); !os.IsNotExist(err) {
		t.Fatalf("uncommitted preparation touched canonical: %v", err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("uncommitted preparation leaked files: %v, %v", entries, err)
	}
}

func TestSpriteBIFAliasByteLimitCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.OpenFile("large.bif", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(spriteMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSpriteBIFAlias(root, "large.bif", "movie.bif"); err == nil {
		t.Fatal("oversized BIF accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "large.bif" {
		t.Fatalf("oversized copy leaked temporary files: %v, %v", entries, err)
	}
}

func TestSpriteBIFAliasRollbackPreservesForeignReplacementAndBackup(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for name, data := range map[string]string{"backing.bif": "new", "movie.bif": "old", "foreign.bif": "foreign"} {
		if err := root.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := prepareSpriteBIFAlias(root, "backing.bif", "movie.bif")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.commit(); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("foreign.bif", "movie.bif"); err != nil {
		t.Fatal(err)
	}
	if err := a.finish(false); err == nil {
		t.Fatal("rollback discarded the foreign replacement error")
	}
	data, err := root.ReadFile("movie.bif")
	if err != nil || string(data) != "foreign" {
		t.Fatalf("rollback clobbered foreign replacement: %q, %v", data, err)
	}
	data, err = root.ReadFile(a.backup)
	if err != nil || string(data) != "old" {
		t.Fatalf("failed rollback lost original backup: %q, %v", data, err)
	}
}
