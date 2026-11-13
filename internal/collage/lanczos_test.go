package collage

import (
	"image"
	"image/color"
	"testing"
	"time"
)

// solid 生成一张纯色图。
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// assertUniform 断言整张图每个像素都与 want 相差不超过 tol。
func assertUniform(t *testing.T, img *image.RGBA, want color.RGBA, tol int) {
	t.Helper()
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			got := rgbaAt(img, x, y)
			if absInt(int(got.R)-int(want.R)) > tol ||
				absInt(int(got.G)-int(want.G)) > tol ||
				absInt(int(got.B)-int(want.B)) > tol ||
				absInt(int(got.A)-int(want.A)) > tol {
				t.Fatalf("(%d,%d) = %+v, 期望 ≈%+v (tol=%d)", x, y, got, want, tol)
			}
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// 纯色图重采样后必须仍是纯色：权重归一化没做好就会在边缘/整体压暗（振铃黑边）。
func TestResizeSolidColorNoRinging(t *testing.T) {
	cases := []struct{ sw, sh, dw, dh int }{
		{100, 100, 30, 30},   // 缩小
		{30, 30, 150, 252},   // 放大
		{300, 450, 168, 252}, // 海报列
		{640, 360, 150, 126}, // 倒影
		{1, 1, 40, 40},       // 1px 放大
		{1000, 1, 20, 5},     // 极端长条
		{7, 13, 3, 5},        // 质数尺寸
	}
	for _, c := range cases {
		src := solid(c.sw, c.sh, color.RGBA{200, 100, 50, 255})
		out := resizeLanczos3(src, c.dw, c.dh)
		if out == nil {
			t.Fatalf("%v: 返回 nil", c)
		}
		if out.Bounds() != image.Rect(0, 0, c.dw, c.dh) {
			t.Fatalf("%v: 尺寸 %v", c, out.Bounds())
		}
		assertUniform(t, out, color.RGBA{200, 100, 50, 255}, 1)
	}
}

// 同尺寸重采样应当近似恒等（Lanczos 在整数点上过零）。
func TestResizeIdentity(t *testing.T) {
	src := poster(64, 96, palette[3])
	out := resizeLanczos3(src, 64, 96)
	if out == nil {
		t.Fatal("返回 nil")
	}
	for y := 0; y < 96; y++ {
		for x := 0; x < 64; x++ {
			want := rgbaAt(src, x, y)
			got := rgbaAt(out, x, y)
			if absInt(int(got.R)-int(want.R)) > 1 ||
				absInt(int(got.G)-int(want.G)) > 1 ||
				absInt(int(got.B)-int(want.B)) > 1 {
				t.Fatalf("(%d,%d) = %+v, 期望 ≈%+v", x, y, got, want)
			}
		}
	}
}

// 缩小后整体亮度必须守恒：说明缩小真的做了抗混叠平均，而不是抽样。
func TestResizeDownscalePreservesMean(t *testing.T) {
	// 1px 黑白相间的棋盘：抽样会保留极端值，抗混叠会收敛到灰。
	src := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			if (x+y)%2 == 0 {
				src.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
			} else {
				src.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
			}
		}
	}
	out := resizeLanczos3(src, 12, 12)
	if out == nil {
		t.Fatal("返回 nil")
	}
	assertUniform(t, out, color.RGBA{128, 128, 128, 255}, 2)
}

// 半透明像素的 alpha 必须跟着走，颜色不能被预乘污染。
// 注意 image.RGBA 存的是预乘值，所以各通道必须 <= alpha。
func TestResizePreservesAlpha(t *testing.T) {
	src := solid(40, 40, color.RGBA{10, 100, 30, 128})
	out := resizeLanczos3(src, 20, 60)
	if out == nil {
		t.Fatal("返回 nil")
	}
	assertUniform(t, out, color.RGBA{10, 100, 30, 128}, 2)
}

// 任意 image.Image 实现都要能吃下：YCbCr（JPEG 解码产物）与 NRGBA。
func TestResizeAcceptsOtherImageTypes(t *testing.T) {
	// YCbCr：用 4:2:0 子采样，覆盖 draw 的转换快速路径。
	ycbcr := image.NewYCbCr(image.Rect(0, 0, 64, 64), image.YCbCrSubsampleRatio420)
	for i := range ycbcr.Y {
		ycbcr.Y[i] = 128
	}
	for i := range ycbcr.Cb {
		ycbcr.Cb[i] = 128
		ycbcr.Cr[i] = 128
	}
	out := resizeLanczos3(ycbcr, 30, 20)
	if out == nil {
		t.Fatal("YCbCr: 返回 nil")
	}
	assertUniform(t, out, color.RGBA{128, 128, 128, 255}, 2)

	nrgba := image.NewNRGBA(image.Rect(0, 0, 33, 21))
	for y := 0; y < 21; y++ {
		for x := 0; x < 33; x++ {
			nrgba.SetNRGBA(x, y, color.NRGBA{40, 60, 80, 255})
		}
	}
	out = resizeLanczos3(nrgba, 77, 9)
	if out == nil {
		t.Fatal("NRGBA: 返回 nil")
	}
	assertUniform(t, out, color.RGBA{40, 60, 80, 255}, 1)
}

// 带偏移的源图（SubImage）必须按自身 Bounds 取样，不能错位。
func TestResizeSubImage(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 60, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 60; x++ {
			base.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	// 只在 [20,40)×[20,40) 里放白色。
	for y := 20; y < 40; y++ {
		for x := 20; x < 40; x++ {
			base.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	sub := base.SubImage(image.Rect(20, 20, 40, 40))
	out := resizeLanczos3(sub, 10, 10)
	if out == nil {
		t.Fatal("返回 nil")
	}
	assertUniform(t, out, color.RGBA{255, 255, 255, 255}, 1)
}

func TestResizeInvalidSize(t *testing.T) {
	src := solid(10, 10, palette[0])
	for _, c := range [][2]int{{0, 10}, {10, 0}, {-1, 5}, {5, -1}} {
		if got := resizeLanczos3(src, c[0], c[1]); got != nil {
			t.Fatalf("size %v 应返回 nil", c)
		}
	}
}

// 单张 640×360 输出的耗时必须有数量级余量：这个断言故意宽松（1s），
// 只用来拦住意外的 O(n³) 实现，具体数字看 BenchmarkResizeLanczos3。
func TestResizePerformanceBudget(t *testing.T) {
	src := poster(300, 450, palette[0])
	start := time.Now()
	if out := resizeLanczos3(src, 640, 360); out == nil {
		t.Fatal("返回 nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("640×360 重采样耗时 %v，超出预算", elapsed)
	}
}

func BenchmarkResizeLanczos3(b *testing.B) {
	src := poster(300, 450, palette[0])
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if resizeLanczos3(src, 150, 252) == nil {
			b.Fatal("nil")
		}
	}
}
