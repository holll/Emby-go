package scraper

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"emby-go/internal/imageutil"
)

func avatarPNG(t *testing.T, n int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8+n%5, 8+n%7))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.Set(x, y, color.RGBA{R: uint8(n * 7), G: uint8(x * 19), B: uint8(y * 17), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAvatarWebPTemporaryIsolation(t *testing.T) {
	for _, kind := range []string{"regular", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "actor.webp")
			legacy := dest + ".tmp"
			guard := filepath.Join(dir, "guard")
			const sentinel = "untouched existing file"
			if err := os.WriteFile(guard, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(guard, legacy); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			} else {
				if err := os.WriteFile(legacy, []byte(sentinel), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			input := avatarPNG(t, 1)
			if err := writeWebP(input, dest); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{guard, legacy} {
				b, err := os.ReadFile(p)
				if err != nil || string(b) != sentinel {
					t.Fatalf("foreign temporary file changed %s: %q %v", p, b, err)
				}
			}
			saved, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			if err = writeWebP([]byte("bad image"), dest); err == nil {
				t.Fatal("invalid image accepted")
			}
			after, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(saved, after) {
				t.Fatalf("failed conversion changed final image: %v", err)
			}
			for _, p := range []string{guard, legacy} {
				b, err := os.ReadFile(p)
				if err != nil || string(b) != sentinel {
					t.Fatalf("failure removed foreign temporary %s", p)
				}
			}
			// Same creation-mode semantics as os.Create, without changing process umask.
			reference := filepath.Join(dir, "mode-reference")
			if err = os.WriteFile(reference, []byte("x"), 0o666); err != nil {
				t.Fatal(err)
			}
			a, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.Stat(reference)
			if err != nil {
				t.Fatal(err)
			}
			if a.Mode().Perm() != b.Mode().Perm() {
				t.Fatalf("creation permissions changed: %v / %v", a.Mode(), b.Mode())
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 4 {
				t.Fatalf("temporary leak: %v %v", entries, err)
			}
		})
	}
}

func TestAvatarWebPConcurrentWritersPublishCompleteImage(t *testing.T) {
	const count = 32
	dir := t.TempDir()
	dest := filepath.Join(dir, "actor.webp")
	inputs := make([][]byte, count)
	expected := map[string]bool{}
	for n := range count {
		inputs[n] = avatarPNG(t, n)
		var b bytes.Buffer
		if err := imageutil.EncodeWebPTo(bytes.NewReader(inputs[n]), &b); err != nil {
			t.Fatal(err)
		}
		expected[b.String()] = true
	}
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for n := range count {
		wg.Go(func() { <-start; errs <- writeWebP(inputs[n], dest) })
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}
	b, err := os.ReadFile(dest)
	if err != nil || !expected[string(b)] {
		t.Fatalf("published mixed/incomplete image: %d bytes %v", len(b), err)
	}
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil || format != "webp" || img.Bounds().Dx() < 8 {
		t.Fatalf("invalid final webp %s %v", format, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "actor.webp" {
		t.Fatalf("temporary leak: %v %v", entries, err)
	}
}

func TestAvatarWebPRenameFailureCleansOwnedTemporary(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "actor.webp")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(dest, "guard")
	if err := os.WriteFile(guard, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeWebP(avatarPNG(t, 1), dest); err == nil {
		t.Fatal("rename onto nonempty directory unexpectedly succeeded")
	}
	b, err := os.ReadFile(guard)
	if err != nil || string(b) != "keep" {
		t.Fatalf("destination changed: %q %v", b, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("rename failure leaked tmp: %v %v", entries, err)
	}
	missing := filepath.Join(dir, "missing", "actor.webp")
	if err := writeWebP(avatarPNG(t, 2), missing); err == nil {
		t.Fatal(fmt.Sprintf("missing parent unexpectedly created: %s", missing))
	}
}
