package imageutil

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePosterFallbackFromFolder(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	file, err := os.Create(filepath.Join(dir, "folder.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	_ = jpeg.Encode(file, img, nil)
	file.Close()

	path, err := EnsurePoster(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "poster.webp" {
		t.Fatalf("expected poster.webp, got %s", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "poster.webp")); err != nil {
		t.Fatalf("poster.webp not created: %v", err)
	}
	// 源 folder.jpg 应被清理
	if _, err := os.Stat(filepath.Join(dir, "folder.jpg")); !os.IsNotExist(err) {
		t.Fatal("folder.jpg should be removed after conversion")
	}
}

func TestEnsurePosterExistingPosterWins(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	f, _ := os.Create(filepath.Join(dir, "folder.jpg"))
	_ = jpeg.Encode(f, img, nil)
	f.Close()

	// 先转换 poster.jpg → poster.webp
	pf, _ := os.Create(filepath.Join(dir, "poster.jpg"))
	_ = jpeg.Encode(pf, img, nil)
	pf.Close()
	if _, err := EnsurePoster(dir); err != nil {
		t.Fatal(err)
	}
	// 已有 poster.webp 时优先返回它，folder.webp 不应生成
	if _, err := os.Stat(filepath.Join(dir, "poster.webp")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "folder.webp")); !os.IsNotExist(err) {
		t.Fatal("folder.webp should not exist when poster.webp present")
	}
}
