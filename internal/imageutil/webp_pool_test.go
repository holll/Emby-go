package imageutil

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// downscaleLegacy 是 P0-1 改造前的缩放实现（全尺寸 RGBA 双缓冲 + 同样的盒子平均），
// 只用于在测试里做逐像素基准：改后的条带路径必须和它输出完全一致。
func downscaleLegacy(src image.Image, maxWidth, maxHeight int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return src
	}
	targetWidth, targetHeight, ok := scaleTarget(width, height, maxWidth, maxHeight)
	if !ok {
		return src
	}
	var source *image.RGBA
	if rgba, isRGBA := src.(*image.RGBA); isRGBA {
		source = rgba
	} else {
		source = image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(source, source.Bounds(), src, bounds.Min, draw.Src)
	}
	destination := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := range targetHeight {
		startY, endY := y*height/targetHeight, (y+1)*height/targetHeight
		if endY <= startY {
			endY = startY + 1
		}
		for x := range targetWidth {
			startX, endX := x*width/targetWidth, (x+1)*width/targetWidth
			if endX <= startX {
				endX = startX + 1
			}
			var red, green, blue, alpha, count uint64
			for sy := startY; sy < endY; sy++ {
				row := source.Pix[sy*source.Stride:]
				for sx := startX; sx < endX; sx++ {
					offset := sx * 4
					red += uint64(row[offset])
					green += uint64(row[offset+1])
					blue += uint64(row[offset+2])
					alpha += uint64(row[offset+3])
					count++
				}
			}
			offset := y*destination.Stride + x*4
			destination.Pix[offset] = uint8(red / count)
			destination.Pix[offset+1] = uint8(green / count)
			destination.Pix[offset+2] = uint8(blue / count)
			destination.Pix[offset+3] = uint8(alpha / count)
		}
	}
	return destination
}

// tpGradient 生成一张确定性的彩色测试图（宽高可变，带梯度与半透明块）。
func tpGradient(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			alpha := uint8(255)
			if (x/7+y/5)%3 == 0 {
				alpha = uint8(90 + (x*13+y*7)%120)
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 255 / max(1, width-1)),
				G: uint8(y * 255 / max(1, height-1)),
				B: uint8((x*3 + y*5) % 256),
				A: alpha,
			})
		}
	}
	return img
}

// writePNGVariant 把不同具体类型的图编码成 PNG/JPEG 文件，覆盖解码器的各条输出路径。
func writeImageFile(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var encErr error
	src := tpGradient(97, 61)
	switch filepath.Ext(path) {
	case ".png":
		encErr = png.Encode(file, src)
	case ".jpg":
		encErr = jpeg.Encode(file, src, &jpeg.Options{Quality: 88})
	default:
		t.Fatalf("未知扩展名 %q", path)
	}
	if encErr != nil {
		t.Fatal(encErr)
	}
}

// TestScalePooledMatchesLegacy 是 P0-1 的像素等价闸门：条带池化路径与改造前的
// 全尺寸双缓冲实现对每一种源类型、每一种缩放比都必须逐字节相同。
func TestScalePooledMatchesLegacy(t *testing.T) {
	// fill 把梯度图转进目标类型，保证每种源都是「有图案的」，串色/错行才会露出来。
	fill := func(dst draw.Image) image.Image {
		draw.Draw(dst, dst.Bounds(), tpGradient(dst.Bounds().Dx(), dst.Bounds().Dy()), image.Point{}, draw.Src)
		return dst
	}
	palette := image.NewPaletted(image.Rect(0, 0, 120, 90), color.Palette{
		color.Black, color.White, color.RGBA{R: 200, A: 255}, color.RGBA{G: 180, B: 60, A: 255},
	})
	sources := map[string]image.Image{
		// 解码器实际会返回的几种具体类型。
		"NRGBA": fill(image.NewNRGBA(image.Rect(0, 0, 400, 300))),
		"RGBA":  fill(image.NewRGBA(image.Rect(0, 0, 400, 300))),
		"Gray":  fill(image.NewGray(image.Rect(0, 0, 233, 151))),
		// 调色板用位置取色，避免全 0 索引退化成纯色。
		"Paletted": func() image.Image {
			for y := range 90 {
				for x := range 120 {
					palette.SetColorIndex(x, y, uint8((x+y)%len(palette.Palette)))
				}
			}
			return palette
		}(),
		// YCbCr 不是 draw.Image，手工填图案（亮度按位置、色度按索引）。
		"YCbCr": func() image.Image {
			img := image.NewYCbCr(image.Rect(0, 0, 640, 360), image.YCbCrSubsampleRatio420)
			for y := range 360 {
				for x := range 640 {
					img.Y[img.YOffset(x, y)] = uint8((x*3 + y*5) % 256)
				}
			}
			for i := range img.Cb {
				img.Cb[i] = uint8(i * 7 % 256)
				img.Cr[i] = uint8(i * 13 % 256)
			}
			return img
		}(),
		"NRGBA64":  fill(image.NewNRGBA64(image.Rect(0, 0, 301, 199))),
		"SubImage": image.Image(nil), // 占位，下面单独构造（带非零原点的子图）
	}
	// 子图：原点不在 (0,0) 的非 RGBA 视图，验证条带 draw 的 sp 偏移。
	parent := tpGradient(200, 160)
	sources["SubImage"] = parent.SubImage(image.Rect(20, 10, 180, 150))

	for name, src := range sources {
		for _, size := range [][2]int{{320, 0}, {61, 0}, {0, 41}, {37, 23}, {1, 1}, {999, 999}} {
			w, h := size[0], size[1]
			// 公共 Downscale 仍是旧语义（调用方持有结果），先确认它也等价。
			gotPublic := Downscale(src, w, h)
			wantLegacy := downscaleLegacy(src, w, h)
			if !samePixels(t, gotPublic, wantLegacy) {
				t.Fatalf("%s %dx%d: Downscale 与旧实现不一致", name, w, h)
			}
			pooled, release, ok := scalePooled(src, w, h)
			if !ok {
				if gotPublic != src {
					t.Fatalf("%s %dx%d: 无需缩放时应原样返回源图", name, w, h)
				}
				continue
			}
			if !samePixels(t, pooled, downscaleLegacy(src, w, h)) {
				release()
				t.Fatalf("%s %dx%d: 池化路径与旧实现不一致", name, w, h)
			}
			release()
		}
	}
}

// samePixels 比较两张图（尺寸 + 每个像素的 RGBA 值）。
func samePixels(t *testing.T, got image.Image, want image.Image) bool {
	t.Helper()
	if !got.Bounds().Size().Eq(want.Bounds().Size()) {
		return false
	}
	if gotRGBA, ok := got.(*image.RGBA); ok {
		if wantRGBA, ok := want.(*image.RGBA); ok {
			return bytes.Equal(gotRGBA.Pix, wantRGBA.Pix)
		}
	}
	b := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if got.At(x, y) != want.At(x, y) {
				return false
			}
		}
	}
	return true
}

// TestThumbnailMatchesLegacyEncoding 断言缩略图接口在文件输入上的输出字节与旧路径完全一致：
// 同解码结果 + 同编码器参数 ⇒ 同 webp 字节（quality 默认值也不许变）。
func TestThumbnailMatchesLegacyEncoding(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"poster.png", "poster.jpg"} {
		path := filepath.Join(dir, name)
		writeImageFile(t, path)
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		img, err := decodeLimited(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range [][2]int{{61, 0}, {0, 41}, {37, 23}, {1, 1}} {
			want, err := EncodeWebPBuffer(downscaleLegacy(img, size[0], size[1]), 0)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Thumbnail(path, size[0], size[1], 0)
			if err != nil {
				t.Fatalf("%s %v: %v", name, size, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s %v: webp 字节与旧路径不同（len %d vs %d）", name, size, len(got), len(want))
			}
		}
		// 不需要缩放时仍返回 (nil, nil)，调用方发原图。
		if data, err := Thumbnail(path, 4000, 4000, 0); err != nil || data != nil {
			t.Fatalf("%s: 无需缩放应返回 nil,nil，得到 %d %v", name, len(data), err)
		}
	}
}

// TestRGBAPoolResetsShape 直接验证池化的关键不变式：取出的缓冲必须按本次尺寸
// 重设 Rect/Stride 并截断 Pix；上一张图的形状不能渗进来。
func TestRGBAPoolResetsShape(t *testing.T) {
	rgbaPool = sync.Pool{New: func() any { return new(image.RGBA) }}
	t.Cleanup(func() {
		rgbaPool = sync.Pool{New: func() any { return new(image.RGBA) }}
	})

	big := getRGBA(160, 120)
	for i := range big.Pix {
		big.Pix[i] = 0xAB // 脏数据：复用后不应出现在有效区域
	}
	putRGBA(big)

	// 复用同一块内存，但形状完全不同。
	small := getRGBA(7, 3)
	if want := image.Rect(0, 0, 7, 3); small.Rect != want {
		t.Fatalf("Rect = %v, 期望 %v", small.Rect, want)
	}
	if small.Stride != 28 {
		t.Fatalf("Stride = %d, 期望 28（4*宽，不是上一张的 640）", small.Stride)
	}
	if len(small.Pix) != 28*3 {
		t.Fatalf("len(Pix) = %d, 期望 %d", len(small.Pix), 28*3)
	}
	if cap(small.Pix) < len(small.Pix) {
		t.Fatal("Pix 容量不足")
	}

	// 超过容量上限的缓冲不进池：下一次取用不应拿到这么大的底层数组。
	oversized := getRGBA(1, 1)
	oversized.Pix = make([]byte, maxPooledRGBABytes+4)
	putRGBA(oversized)
	reused := getRGBA(1, 1)
	if cap(reused.Pix) > maxPooledRGBABytes {
		t.Fatalf("超过上限的缓冲（cap=%d）不应进池", cap(reused.Pix))
	}
	putRGBA(nil) // 不应 panic
}

// TestDecodeLimitedConvertsPanicToError 用接缝把解码器换成会 panic 的实现，
// 断言 P1-3 (a) 的 recover 守卫真的把崩溃转成了 error。
func TestDecodeLimitedConvertsPanicToError(t *testing.T) {
	originalDecode, originalConfig := decodeImage, decodeImageConfig
	t.Cleanup(func() { decodeImage, decodeImageConfig = originalDecode, originalConfig })

	dir := t.TempDir()
	path := filepath.Join(dir, "poster.png")
	writeImageFile(t, path)

	// 1) 解码阶段 panic。
	decodeImage = func(io.Reader) (image.Image, string, error) { panic("decoder exploded") }
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := decodeLimited(file); !errors.Is(err, errDecodePanic) {
		t.Fatalf("解码 panic 应转成 errDecodePanic，得到 %v", err)
	}
	// Thumbnail 也必须返回错误而不是把 panic 抛给调用方。
	if _, err := Thumbnail(path, 32, 0, 0); !errors.Is(err, errDecodePanic) {
		t.Fatalf("Thumbnail 应返回 errDecodePanic，得到 %v", err)
	}

	// 2) 读头阶段 panic。
	decodeImage = originalDecode
	decodeImageConfig = func(io.Reader) (image.Config, string, error) { panic("bad header") }
	if _, err := Thumbnail(path, 32, 0, 0); !errors.Is(err, errDecodePanic) {
		t.Fatalf("读头 panic 应转成 errDecodePanic，得到 %v", err)
	}

	// 3) 守卫不影响正常路径：还原后同一文件必须照旧可用。
	decodeImageConfig = originalConfig
	data, err := Thumbnail(path, 32, 0, 0)
	if err != nil || len(data) == 0 {
		t.Fatalf("还原后应正常生成缩略图: len=%d err=%v", len(data), err)
	}
}

// TestThumbnailMalformedInputs 覆盖畸形/截断输入：不 panic、返回 error，
// 且失败后池不会把脏数据带进下一张图。
func TestThumbnailMalformedInputs(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.png")
	writeImageFile(t, valid)
	validBytes, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	paletted := filepath.Join(dir, "paletted.jpg")
	writeImageFile(t, paletted)
	validJPEG, err := os.ReadFile(paletted)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"空文件":      {},
		"随机字节":     []byte("this is definitely not an image, just bytes 0123456789"),
		"截断 PNG":   validBytes[:len(validBytes)/3],
		"只有 PNG 头": validBytes[:16],
		"截断 JPEG":  validJPEG[:len(validJPEG)/2],
		"损坏的 PNG IDAT": func() []byte {
			corrupt := bytes.Clone(validBytes)
			// 从中间开始抹掉一段：IHDR 还在，IDAT 的数据流坏了。
			for i := len(corrupt) / 2; i < len(corrupt)/2+64 && i < len(corrupt); i++ {
				corrupt[i] ^= 0xFF
			}
			return corrupt
		}(),
	}
	for name, payload := range cases {
		path := filepath.Join(dir, "broken-"+name+".png")
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		// 关键断言：这里不允许 panic（panic 会让整个测试二进制挂掉）。
		data, err := Thumbnail(path, 32, 0, 0)
		if err == nil && len(data) != 0 {
			t.Fatalf("%s: 畸形输入不应生成缩略图（len=%d）", name, len(data))
		}
		// 每次坏图之后，同一进程内正常图片必须仍然可用（池/状态没被污染）。
		good, err := Thumbnail(valid, 32, 0, 0)
		if err != nil || len(good) == 0 {
			t.Fatalf("%s: 坏图之后正常图片生成失败: len=%d err=%v", name, len(good), err)
		}
	}
}

// TestThumbnailPoolDoesNotLeakPixels 连续处理尺寸差异很大的图，每张都必须与
// 「每次新分配缓冲的旧路径」逐字节相同：Stride/Rect 复用错误或条带残留都会
// 在这里暴露成串色/错行。
func TestThumbnailPoolDoesNotLeakPixels(t *testing.T) {
	dir := t.TempDir()
	writeSolid := func(name string, width, height int, c color.RGBA) string {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := range height {
			for x := range width {
				img.SetRGBA(x, y, c)
			}
		}
		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := png.Encode(file, img); err != nil {
			t.Fatal(err)
		}
		return path
	}
	red := writeSolid("red.png", 421, 233, color.RGBA{R: 255, A: 255})
	blue := writeSolid("blue.png", 91, 57, color.RGBA{B: 255, A: 255})
	green := writeSolid("green.png", 2000, 40, color.RGBA{G: 255, A: 255})
	// 有条纹的图：一旦行/列错位，平均结果必然变，纯色图反而看不出来。
	striped := filepath.Join(dir, "striped.png")
	stripedImg := image.NewRGBA(image.Rect(0, 0, 511, 301))
	for y := range 301 {
		for x := range 511 {
			if (x/3+y/2)%2 == 0 {
				stripedImg.SetRGBA(x, y, color.RGBA{R: 250, A: 255})
			} else {
				stripedImg.SetRGBA(x, y, color.RGBA{B: 250, A: 255})
			}
		}
	}
	stripedFile, err := os.Create(striped)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(stripedFile, stripedImg); err != nil {
		stripedFile.Close()
		t.Fatal(err)
	}
	stripedFile.Close()

	// 大 → 小 → 长条 → 细纹：故意让池里的缓冲形状在四种尺寸间来回换。
	cases := []struct {
		path  string
		width int
	}{
		{red, 40},
		{blue, 40},
		{green, 40},
		{striped, 40},
		{blue, 7},
		{striped, 23},
	}
	reference := func(path string, width int) []byte {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		src, err := decodeLimited(file)
		if err != nil {
			t.Fatal(err)
		}
		want, err := EncodeWebPBuffer(downscaleLegacy(src, width, 0), 0)
		if err != nil {
			t.Fatal(err)
		}
		return want
	}
	for round := range 3 { // 多轮，确保走的是池里复用出来的缓冲
		for _, tc := range cases {
			want := reference(tc.path, tc.width)
			got, err := Thumbnail(tc.path, tc.width, 0, 0)
			if err != nil || len(got) == 0 {
				t.Fatalf("round %d %s: len=%d err=%v", round, filepath.Base(tc.path), len(got), err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("round %d %s maxWidth=%d: 缩略图字节与旧路径不同（%d vs %d 字节）",
					round, filepath.Base(tc.path), tc.width, len(got), len(want))
			}
		}
	}
}
