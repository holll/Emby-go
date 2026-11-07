package regularfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularDocument(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "document")
	if err := os.WriteFile(path, []byte("document bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(got) != "document bytes" {
		t.Fatalf("read=%q error=%v", got, err)
	}
	if file, err := Open(root); !errors.Is(err, ErrNotRegular) || file != nil {
		t.Fatalf("directory: file=%v error=%v", file, err)
	}
	if file, err := Open(path + ".missing"); !errors.Is(err, os.ErrNotExist) || file != nil {
		t.Fatalf("missing: file=%v error=%v", file, err)
	}
	t.Run("file_symlinks", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(path, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		file, err := Open(link)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(got) != "document bytes" {
			t.Fatalf("symlink read=%q error=%v", got, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if file, err := Open(link); !errors.Is(err, os.ErrNotExist) || file != nil {
			t.Fatalf("broken symlink: file=%v error=%v", file, err)
		}
	})
}
