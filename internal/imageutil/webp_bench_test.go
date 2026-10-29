package imageutil

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// benchSourceWidth/Height 是基准测试的源图尺寸：1920×1080 的整幅海报/背景图，
// 与真实库里 poster/fanart 量级一致（全尺寸 RGBA 单缓冲 8.29 MB）。
const (
	benchSourceWidth  = 1920
	benchSourceHeight = 1080
)

// benchImage 生成一张确定性的源图：渐变 + 棋盘 + 伪随机噪点，
// 保证每个像素都不一样（纯色图会被编码器压得过于简单，基准失真）。
func benchImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, benchSourceWidth, benchSourceHeight))
	seed := uint32(2463534242)
	for y := range benchSourceHeight {
		for x := range benchSourceWidth {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			noise := uint8(seed >> 24)
			var value uint8
			if (x/16+y/16)%2 == 0 {
				value = 32
			}
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x*255/benchSourceWidth)/2 + value + noise/8,
				G: uint8(y*255/benchSourceHeight)/2 + value + noise/8,
				B: 128 + noise/4,
				A: 255,
			})
		}
	}
	return img
}

// benchSourcePath 把基准源图写进临时目录并按扩展名编码；不依赖任何外部 fixture。
func benchSourcePath(b *testing.B, name string) string {
	b.Helper()
	dir := b.TempDir()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	img := benchImage()
	switch filepath.Ext(name) {
	case ".png":
		err = png.Encode(file, img)
	case ".jpg":
		err = jpeg.Encode(file, img, &jpeg.Options{Quality: 90})
	default:
		err = os.ErrInvalid
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		b.Fatal(err)
	}
	if info, err := os.Stat(path); err == nil {
		b.Logf("benchmark 源图 %s: %d 字节, %dx%d", name, info.Size(), benchSourceWidth, benchSourceHeight)
	}
	return path
}

// BenchmarkThumbnail320WebP 是 P0-1 的回归基准：1920×1080 PNG 源 → 320px webp，
// 与报告里 217–269 ms/op、14.2 MB/op 的场景同一条调用链（Thumbnail 全流程）。
func BenchmarkThumbnail320WebP(b *testing.B) {
	path := benchSourcePath(b, "poster.png")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data, err := Thumbnail(path, 320, 0, 0)
		if err != nil || len(data) == 0 {
			b.Fatalf("Thumbnail: %v len=%d", err, len(data))
		}
	}
}

// BenchmarkThumbnail320WebPJPEG 覆盖另一条解码路径（*image.YCbCr → RGBA）。
func BenchmarkThumbnail320WebPJPEG(b *testing.B) {
	path := benchSourcePath(b, "poster.jpg")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data, err := Thumbnail(path, 320, 0, 0)
		if err != nil || len(data) == 0 {
			b.Fatalf("Thumbnail: %v len=%d", err, len(data))
		}
	}
}

// BenchmarkDownscale320 只测缩放段，用来区分「解码」与「缩放 + 编码」的归属。
func BenchmarkDownscale320(b *testing.B) {
	img := benchImage()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if out := Downscale(img, 320, 0); out == nil {
			b.Fatal("Downscale 返回 nil")
		}
	}
}

// thumbnailLegacy 是 P0-1 改造前的整条缩略图路径：解码 → 全尺寸 RGBA 双缓冲缩放
// → 编码。它和现在的 Thumbnail 只差「缓冲是否复用」，所以同进程交替测量就能把
// 池化本身的收益单独摘出来（不依赖 git 也不需要旧二进制）。
func thumbnailLegacy(path string, maxWidth, maxHeight, quality int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, err := decodeLimited(file)
	if err != nil {
		return nil, err
	}
	scaled := downscaleLegacy(img, maxWidth, maxHeight)
	if scaled == img {
		return nil, nil
	}
	return EncodeWebPBuffer(scaled, quality)
}

// BenchmarkThumbnail320WebPLegacy 是改造前基线（每次请求新分配全尺寸 RGBA）。
func BenchmarkThumbnail320WebPLegacy(b *testing.B) {
	path := benchSourcePath(b, "poster.png")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data, err := thumbnailLegacy(path, 320, 0, 0)
		if err != nil || len(data) == 0 {
			b.Fatalf("thumbnailLegacy: %v len=%d", err, len(data))
		}
	}
}

// BenchmarkThumbnail320WebPLegacyJPEG 是改造前基线（jpeg 源：YCbCr 解码 + 全尺寸 RGBA 复制）。
func BenchmarkThumbnail320WebPLegacyJPEG(b *testing.B) {
	path := benchSourcePath(b, "poster.jpg")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data, err := thumbnailLegacy(path, 320, 0, 0)
		if err != nil || len(data) == 0 {
			b.Fatalf("thumbnailLegacy: %v len=%d", err, len(data))
		}
	}
}
