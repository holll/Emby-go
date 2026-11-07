package imageutil

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
)

func TestEncodeJPEGToDefaultQualityAndErrors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 18))
	var src bytes.Buffer
	if err := png.Encode(&src, img); err != nil {
		t.Fatal(err)
	}
	var got, want bytes.Buffer
	if err := EncodeJPEGTo(bytes.NewReader(src.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&want, img, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatal("JPEG differs from standard library default quality")
	}
	decoded, format, err := image.Decode(bytes.NewReader(got.Bytes()))
	if err != nil || format != "jpeg" || decoded.Bounds() != img.Bounds() {
		t.Fatalf("decode = %s, %v", format, err)
	}
	var invalid bytes.Buffer
	if err := EncodeJPEGTo(bytes.NewReader([]byte("not an image")), &invalid); err == nil || invalid.Len() != 0 {
		t.Fatalf("invalid input: %v, bytes=%d", err, invalid.Len())
	}
	sentinel := errors.New("writer failed")
	if err := EncodeJPEGTo(bytes.NewReader(src.Bytes()), failingWebPWriter{sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("writer error = %v", err)
	}
}

func TestEncodeJPEGToSharedDecodeBounds(t *testing.T) {
	originalConfig, originalDecode := decodeImageConfig, decodeImage
	t.Cleanup(func() { decodeImageConfig, decodeImage = originalConfig, originalDecode })
	decodeImageConfig = func(io.Reader) (image.Config, string, error) {
		return image.Config{Width: 10_000, Height: 10_000}, "png", nil
	}
	decodeImage = func(io.Reader) (image.Image, string, error) {
		t.Fatal("oversized image reached full decode")
		return nil, "", nil
	}
	var dst bytes.Buffer
	if err := EncodeJPEGTo(bytes.NewReader(nil), &dst); !errors.Is(err, errImageTooLarge) || dst.Len() != 0 {
		t.Fatalf("oversized input: %v, bytes=%d", err, dst.Len())
	}
}
