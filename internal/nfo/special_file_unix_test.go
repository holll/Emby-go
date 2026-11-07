//go:build unix

package nfo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"emby-go/internal/regularfile"
)

func TestNFOFIFORejected(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"ReadFile", "Read", "TagPreview", "ListPreview", "ReadTranslationSnapshot", "UpdateScraped", "UpdateScrapedWithAutoMarks", "SaveFileInfo", "SetActorThumb", "CommitTranslation"} {
		for _, kind := range []string{"direct", "symlink"} {
			t.Run(operation+"/"+kind, func(t *testing.T) {
				// An accidental blocking open must fail this test, not hang the
				// package or leave a goroutine holding its NFO path lock.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestNFOSpecialFileChild$", "-test.v")
				cmd.Env = append(os.Environ(), "EMBY_NFO_SPECIAL_OPERATION="+operation, "EMBY_NFO_SPECIAL_KIND="+kind, "EMBY_NFO_SPECIAL_DIR="+t.TempDir(), "GORACE=atexit_sleep_ms=0")
				output, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("FIFO operation blocked until watchdog: %v\n%s", ctx.Err(), output)
				}
				if err != nil {
					t.Fatalf("FIFO child failed: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestNFOSpecialFileChild(t *testing.T) {
	operation := os.Getenv("EMBY_NFO_SPECIAL_OPERATION")
	if operation == "" {
		t.Skip("isolated FIFO operation helper")
	}
	dir := os.Getenv("EMBY_NFO_SPECIAL_DIR")
	path := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(path, []byte("<movie><title>old</title><originaltitle>source</originaltitle></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Obtain a valid optimistic token first: CommitTranslation must reach the
	// reread boundary rather than reject an unrelated invalid snapshot.
	snap, err := ReadTranslationSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fifo := path
	linked := os.Getenv("EMBY_NFO_SPECIAL_KIND") == "symlink"
	if linked {
		fifo = filepath.Join(dir, "target.fifo")
	}
	// No reader/writer peer is opened. A plain os.Open(O_RDONLY) would wait
	// forever here, before any byte limit could apply.
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if linked {
		if err := os.Symlink(fifo, path); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Lstat(fifo)
	if err != nil {
		t.Fatal(err)
	}
	switch operation {
	case "ReadFile":
		var data []byte
		data, err = ReadFile(path)
		if data != nil {
			t.Fatalf("failed read returned bytes: %q", data)
		}
	case "Read":
		_, err = Read(path)
	case "TagPreview":
		_, err = TagPreview(path, []string{"title"})
	case "ListPreview":
		_, _, err = ListPreview(path)
	case "ReadTranslationSnapshot":
		_, err = ReadTranslationSnapshot(path)
	case "UpdateScraped":
		err = UpdateScraped(path, ScrapeFields{Overwrite: true, Title: "updated"})
	case "UpdateScrapedWithAutoMarks":
		var owned []string
		owned, err = UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: true, Title: "updated"}, []string{"C"}, []string{"UC"})
		if owned != nil {
			t.Fatalf("failed update returned ownership: %v", owned)
		}
	case "SaveFileInfo":
		err = SaveFileInfo(path, FileInfoMeta{Size: 123}, &StreamDetails{})
	case "SetActorThumb":
		var changed bool
		changed, err = SetActorThumb(path, "actor", "updated.jpg")
		if changed {
			t.Fatal("failed actor update reported changed")
		}
	case "CommitTranslation":
		var changed bool
		changed, err = CommitTranslation(context.Background(), path, snap, "updated", "")
		if changed {
			t.Fatal("failed translation reported changed")
		}
	default:
		t.Fatalf("unknown operation %q", operation)
	}
	if !errors.Is(err, regularfile.ErrNotRegular) {
		t.Fatalf("got %v, want ErrNotRegular", err)
	}
	after, err := os.Lstat(fifo)
	if err != nil || after.Mode()&os.ModeNamedPipe == 0 || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("FIFO was replaced or modified: %v, %v", after, err)
	}
	if linked {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link was replaced: %v, %v", info, err)
		}
		target, err := os.Readlink(path)
		if err != nil || target != fifo {
			t.Fatalf("link target changed: %q, %v", target, err)
		}
	}
	entries, err := os.ReadDir(dir)
	wantEntries := 1
	if linked {
		wantEntries = 2
	}
	if err != nil || len(entries) != wantEntries {
		t.Fatalf("rejected operation left temporary files: %v, %v", entries, err)
	}
}
