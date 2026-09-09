package imageutil

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFindPosterFolderSourceKept(t *testing.T) {
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "folder.jpg"))

	path := FindPoster(dir)
	if path == "" || filepath.Base(path) != "folder.jpg" {
		t.Fatalf("expected folder.jpg, got %q", path)
	}
	// 不生成 webp、不删除源文件
	if _, err := os.Stat(filepath.Join(dir, "poster.webp")); !os.IsNotExist(err) {
		t.Fatal("poster.webp should not be generated")
	}
	if _, err := os.Stat(filepath.Join(dir, "folder.jpg")); err != nil {
		t.Fatal("folder.jpg source should be kept")
	}
}

func TestFindPosterPriority(t *testing.T) {
	dir := t.TempDir()
	// 已有 webp（旧版本扫描产物）应优先于 folder.jpg
	writeJPEG(t, filepath.Join(dir, "poster.webp"))
	writeJPEG(t, filepath.Join(dir, "folder.jpg"))

	if path := FindPoster(dir); filepath.Base(path) != "poster.webp" {
		t.Fatalf("expected poster.webp to win, got %q", path)
	}

	// 无任何海报 → 空串
	empty := t.TempDir()
	if path := FindPoster(empty); path != "" {
		t.Fatalf("expected empty, got %q", path)
	}

	// 只有 cover.webp 时也能作为库根封面被识别
	coverDir := t.TempDir()
	writeJPEG(t, filepath.Join(coverDir, "cover.webp"))
	if path := FindPoster(coverDir); filepath.Base(path) != "cover.webp" {
		t.Fatalf("expected cover.webp, got %q", path)
	}
}

func TestAspectRatioWebP(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 6, 3))
	file, _ := os.Create(src)
	_ = jpeg.Encode(file, img, nil)
	file.Close()

	out := filepath.Join(dir, "cover.webp")
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := EncodeWebP(in, out); err != nil {
		in.Close()
		t.Fatal(err)
	}
	in.Close()

	if ratio := AspectRatio(out); ratio < 1.9 || ratio > 2.1 {
		t.Fatalf("webp aspect ratio = %v, want ~2.0", ratio)
	}
	if ratio := AspectRatio(src); ratio < 1.9 || ratio > 2.1 {
		t.Fatalf("jpeg aspect ratio = %v, want ~2.0", ratio)
	}
	if ratio := AspectRatio(filepath.Join(dir, "missing.webp")); ratio != 0 {
		t.Fatalf("missing file ratio = %v, want 0", ratio)
	}
}

func TestFindImage(t *testing.T) {
	dir := t.TempDir()
	writeJPEG(t, filepath.Join(dir, "fanart.jpg"))
	if path := FindImage(dir, "fanart"); filepath.Base(path) != "fanart.jpg" {
		t.Fatalf("expected fanart.jpg, got %q", path)
	}
	writeJPEG(t, filepath.Join(dir, "landscape.png"))
	if path := FindImage(dir, "landscape"); filepath.Base(path) != "landscape.png" {
		t.Fatalf("expected landscape.png, got %q", path)
	}
	if path := FindImage(dir, "backdrop"); path != "" {
		t.Fatalf("expected empty, got %q", path)
	}
}
