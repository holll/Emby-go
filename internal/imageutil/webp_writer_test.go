package imageutil

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

type failingWebPWriter struct{ err error }

func (w failingWebPWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestEncodeWebPToMatchesPathAndPropagatesErrors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var src bytes.Buffer
	if err := png.Encode(&src, img); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := EncodeWebPTo(bytes.NewReader(src.Bytes()), &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.webp")
	if err := EncodeWebP(bytes.NewReader(src.Bytes()), path); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(legacy, output.Bytes()) {
		t.Fatalf("encoding differs: %v", err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(output.Bytes()))
	if err != nil || format != "webp" || decoded.Bounds() != img.Bounds() {
		t.Fatalf("decode %s %v", format, err)
	}
	var invalid bytes.Buffer
	if err = EncodeWebPTo(bytes.NewReader([]byte("not an image")), &invalid); err == nil || invalid.Len() != 0 {
		t.Fatalf("invalid input: %v bytes=%d", err, invalid.Len())
	}
	sentinel := errors.New("fixture writer rejected bytes")
	if err = EncodeWebPTo(bytes.NewReader(src.Bytes()), failingWebPWriter{sentinel}); err == nil {
		t.Fatal("writer error ignored")
	}
}
