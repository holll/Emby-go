//go:build unix

package regularfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenSpecialFileChild(t *testing.T) {
	if os.Getenv("EMBY_REGULARFILE_CHILD") != "1" {
		t.Skip("special-file subprocess only")
	}
	root := t.TempDir()
	fifo := filepath.Join(root, "document.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual Unix flag separately: after a pre-Stat replacement,
	// opening this FIFO with no writer must not wait for a writer to arrive.
	file, err := os.OpenFile(fifo, os.O_RDONLY|nonblock, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	link := filepath.Join(root, "link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, link, "/dev/null"} {
		file, err := Open(path)
		if !errors.Is(err, ErrNotRegular) || file != nil {
			t.Fatalf("special path %s: file=%v error=%v", path, file, err)
		}
	}
}

func TestOpenSpecialFilesDoNotWaitForWriters(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestOpenSpecialFileChild$", "-test.v")
	cmd.Env = append(os.Environ(), "EMBY_REGULARFILE_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("special-file child: %v (context=%v)\n%s", err, ctx.Err(), output)
	}
	t.Logf("special-file child:\n%s", output)
}
