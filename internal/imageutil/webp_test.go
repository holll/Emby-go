package imageutil

import (
	"bytes"
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

func TestDownscale(t *testing.T) {
	// 左半红、右半蓝：缩放后左右两色必须仍然分开。
	src := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			pixel := color.RGBA{R: 255, A: 255}
			if x >= 50 {
				pixel = color.RGBA{B: 255, A: 255}
			}
			src.SetRGBA(x, y, pixel)
		}
	}

	out := Downscale(src, 20, 0)
	if out.Bounds().Dx() != 20 || out.Bounds().Dy() != 10 {
		t.Fatalf("按宽缩放尺寸 = %v，期望 20x10", out.Bounds())
	}
	left := color.RGBAModel.Convert(out.At(2, 5)).(color.RGBA)
	right := color.RGBAModel.Convert(out.At(17, 5)).(color.RGBA)
	if left.R != 255 || left.B != 0 {
		t.Errorf("左半应仍为纯红: %+v", left)
	}
	if right.B != 255 || right.R != 0 {
		t.Errorf("右半应仍为纯蓝: %+v", right)
	}

	// 只限高：宽度按比例跟着缩。
	out = Downscale(src, 0, 25)
	if out.Bounds().Dx() != 50 || out.Bounds().Dy() != 25 {
		t.Fatalf("按高缩放尺寸 = %v，期望 50x25", out.Bounds())
	}

	// 目标比原图大时不做放大，原样返回。
	if got := Downscale(src, 500, 500); got != image.Image(src) {
		t.Errorf("不应放大: %v", got.Bounds())
	}
}

func TestEncodeWebPBuffer(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	src.Set(0, 0, color.RGBA{R: 200, G: 100, A: 255})
	data, err := EncodeWebPBuffer(src, 0)
	if err != nil {
		t.Fatalf("EncodeWebPBuffer: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("编码结果为空")
	}
	// 编出来的必须还能解回图片（webp 包已注册到 image 包）。
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if decoded.Bounds().Dx() != 8 {
		t.Errorf("解码宽度 = %d", decoded.Bounds().Dx())
	}
}
