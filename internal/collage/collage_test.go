package collage

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ---------- 测试辅助 ----------

// rgbaAt 读画布上的一个像素。
func rgbaAt(img *image.RGBA, x, y int) color.RGBA {
	i := img.PixOffset(x, y)
	return color.RGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
}

// isBlack 判断像素是否（近似）纯黑：Lanczos 与渐变的定点误差允许几个色阶。
func isBlack(c color.RGBA) bool { return c.R < 8 && c.G < 8 && c.B < 8 }

// isBright 判断像素是否是明显可见的海报内容（而非黑底）。
func isBright(c color.RGBA) bool { return int(c.R)+int(c.G)+int(c.B) > 120 }

// palette 是给合成海报用的高区分度底色，保证相邻列一眼可辨。
var palette = []color.RGBA{
	{0xE0, 0x20, 0x20, 0xFF}, // 红
	{0x20, 0xC0, 0x40, 0xFF}, // 绿
	{0x20, 0x60, 0xE0, 0xFF}, // 蓝
	{0xE0, 0xC0, 0x20, 0xFF}, // 黄
	{0xC0, 0x20, 0xE0, 0xFF}, // 紫
	{0x20, 0xC0, 0xC0, 0xFF}, // 青
	{0xE0, 0x80, 0x20, 0xFF}, // 橙
	{0xF0, 0xF0, 0xF0, 0xFF}, // 近白
	{0x80, 0x40, 0x20, 0xFF}, // 棕
	{0x40, 0x80, 0x40, 0xFF}, // 墨绿
}

// poster 生成一张竖版海报：纯色底 + 中间一条高亮横带，便于断言裁剪位置。
// 横带让它即使被左右裁剪也一定保留可辨认的内容。
func poster(w, h int, base color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, base)
		}
	}
	band := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	for y := h / 3; y < 2*h/3; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, band)
		}
	}
	return img
}

// writePNG 把图片写到 dir/name，返回路径。
func writePNG(t *testing.T, dir, name string, img image.Image) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	return path
}

// posters 生成 n 张 300×450（2:3）的合成海报，返回路径。
func posters(t *testing.T, n int) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		paths[i] = writePNG(t, dir, string(rune('a'+i))+".png", poster(300, 450, palette[i%len(palette)]))
	}
	return paths
}

// columnCenter 返回第 i 列主体的取样点：水平居中、纵向落在合成海报的纯色底
// 区域（避开中间那条白色横带，否则所有列都是白的，看不出是哪张图）。
func columnCenter(g Geometry, i int) (int, int) {
	return g.X[i] + g.Slice/2, g.BodyY + g.BodyHeight/8
}

// ---------- 4. 精确几何 ----------

func TestGeometry640x360(t *testing.T) {
	g := Layout{}.Geometry()

	if g.Slice != 150 || g.BodyHeight != 252 || g.Padding != 8 || g.Spacing != 4 {
		t.Fatalf("几何不符: %+v", g)
	}
	if g.ReflectionHeight != 100 || g.BodyY != 4 || g.ReflectionY != 260 {
		t.Fatalf("纵向几何不符: %+v", g)
	}
	want := []int{8, 166, 324, 482}
	if len(g.X) != len(want) {
		t.Fatalf("X 槽位数 = %d, 期望 %d", len(g.X), len(want))
	}
	for i := range want {
		if g.X[i] != want[i] {
			t.Fatalf("X[%d] = %d, 期望 %d", i, g.X[i], want[i])
		}
	}

	// 显式给默认值必须与零值一致。
	explicit := (Layout{Width: 640, Height: 360, Columns: 4}).Geometry()
	if !reflect.DeepEqual(explicit, g) {
		t.Fatal("显式默认值与零值 Layout 不一致")
	}
}

func TestGeometryHonorsLayout(t *testing.T) {
	g := (Layout{Width: 320, Height: 180}).Geometry()
	if g.Slice != 75 || g.BodyHeight != 126 || g.Padding != 4 || g.Spacing != 2 {
		t.Fatalf("320×180 几何不符: %+v", g)
	}
	if len(g.X) != 4 || g.X[0] != 4 || g.X[3] != 4*4+75*3 {
		t.Fatalf("320×180 槽位不符: %v", g.X)
	}
	if len((Layout{Columns: 2}).Geometry().X) != 2 {
		t.Fatal("Columns=2 时槽位数应为 2")
	}
}

// ---------- 8. 取整语义 ----------

func TestCsharpRoundIsBankersRounding(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{2.5, 2}, // 取偶，int(x+0.5) 会得到 3
		{3.5, 4},
		{0.5, 0},
		{1.5, 2},
		{-0.5, 0},
		{-1.5, -2},
		{-2.5, -2},
		{2.4, 2},
		{2.6, 3},
		{4.0, 4},
	}
	for _, c := range cases {
		if got := csharpRound(c.in); got != c.want {
			t.Errorf("csharpRound(%v) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

// 用能区分 banker's rounding 与 int(x+0.5) 的宽度验证 Geometry 走的是前者：
// 200*0.0125 恰好是 2.5，取偶得 2，而 int(2.5+0.5) 得 3。
func TestGeometryUsesBankersRounding(t *testing.T) {
	if got := (Layout{Width: 200, Height: 360}).Geometry().Padding; got != 2 {
		t.Fatalf("Padding = %d, 期望 2（banker's rounding）", got)
	}
}

// ---------- 1. 槽位与环形补位 ----------

func TestColumnAssignment(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want []int // 每列来自第几张图
	}{
		{"1 张", 1, []int{0, 0, 0, 0}},
		{"2 张", 2, []int{0, 1, 0, 1}},
		{"3 张", 3, []int{0, 1, 2, 0}},
		{"4 张", 4, []int{0, 1, 2, 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paths := posters(t, c.n)
			img, err := Render(paths, Options{Reflection: true})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			g := Layout{}.Geometry()

			colors := make([]color.RGBA, 4)
			for i := range colors {
				x, y := columnCenter(g, i)
				colors[i] = rgbaAt(img, x, y)
				if !isBright(colors[i]) {
					t.Fatalf("第 %d 列中心 (%d,%d) 是黑的: %+v", i, x, y, colors[i])
				}
			}
			for i, want := range c.want {
				for j, other := range c.want {
					same := colors[i] == colors[j]
					if same != (want == other) {
						t.Fatalf("列 %d(图%d) 与 列 %d(图%d) 颜色关系不符: %+v vs %+v",
							i, want, j, other, colors[i], colors[j])
					}
				}
			}
		})
	}
}

func TestManyImages(t *testing.T) {
	for _, n := range []int{5, 6, 7, 8, 9, 10} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			img, err := Render(posters(t, n), Options{Reflection: true})
			if err != nil {
				t.Fatalf("%d 张: %v", n, err)
			}
			if img.Bounds() != image.Rect(0, 0, 640, 360) {
				t.Fatalf("%d 张: 尺寸 %v", n, img.Bounds())
			}
			g := Layout{}.Geometry()
			for i := 0; i < 4; i++ {
				x, y := columnCenter(g, i)
				if c := rgbaAt(img, x, y); !isBright(c) {
					t.Fatalf("%d 张: 第 %d 列是黑的 %+v", n, i, c)
				}
			}
			// 只有前 4 张会被用到，但后面的图不应改变结果。
			fewer, err := Render(posters(t, 4), Options{Reflection: true})
			if err != nil {
				t.Fatalf("%d 张: 对照组失败 %v", n, err)
			}
			if !bytes.Equal(img.Pix, fewer.Pix) {
				t.Fatalf("%d 张: 第 5 张之后参与了绘制", n)
			}
		})
	}
}

// ---------- 2. 各种宽高比 ----------

func TestAspectRatios(t *testing.T) {
	cases := []struct {
		name  string
		w, h  int
		width int // 该列实际绘制宽度 = 252*w/h
	}{
		{"竖版 2:3", 300, 450, 168},
		{"超宽 21:9", 630, 270, 588},
		{"正方形 1:1", 252, 252, 252},
		{"极端 1:10", 100, 1000, 25}, // 触发 subset 越界降级分支
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writePNG(t, dir, "x.png", poster(c.w, c.h, palette[0]))
			img, err := Render([]string{path}, Options{Reflection: true})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if img.Bounds() != image.Rect(0, 0, 640, 360) {
				t.Fatalf("尺寸 %v", img.Bounds())
			}
			g := Layout{}.Geometry()

			// 四列都应有内容：极端比例下只会占 iWidth 宽，这是源码行为。
			for i := 0; i < 4; i++ {
				if c.width < g.Slice {
					// 降级分支：只有 [X[i], X[i]+iWidth) 有像素，且没有倒影。
					if px := rgbaAt(img, g.X[i], g.BodyY+1); !isBright(px) {
						t.Fatalf("列 %d 左边缘应可见: %+v", i, px)
					}
					if px := rgbaAt(img, g.X[i]+c.width-1, g.BodyY+1); !isBright(px) {
						t.Fatalf("列 %d 右边缘应可见: %+v", i, px)
					}
					continue
				}
				x, y := columnCenter(g, i)
				if px := rgbaAt(img, x, y); !isBright(px) {
					t.Fatalf("列 %d 中心应为海报像素: %+v", i, px)
				}
				// padding 与列间隙必须保持纯黑。
				if px := rgbaAt(img, g.X[i]-1, y); !isBlack(px) {
					t.Fatalf("列 %d 左侧 padding 应为黑: %+v", i, px)
				}
			}
		})
	}
}

// ---------- 3. 不可用输入 ----------

func TestNoImages(t *testing.T) {
	if _, err := Render(nil, Options{}); !errors.Is(err, ErrNoImages) {
		t.Fatalf("空 paths: err = %v, 期望 ErrNoImages", err)
	}
	if _, err := Render([]string{}, Options{}); !errors.Is(err, ErrNoImages) {
		t.Fatalf("空切片: err = %v, 期望 ErrNoImages", err)
	}
}

func TestMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.png")
	garbage := bytes.Repeat([]byte{0x13, 0x37, 0x00, 0xFF}, 512)
	if err := os.WriteFile(corrupt, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope.png")

	all := []string{missing, corrupt}
	if _, err := Render(all, Options{}); !errors.Is(err, ErrNoImages) {
		t.Fatalf("全部不可用: err = %v, 期望 ErrNoImages", err)
	}

	// 混入一张好图：坏路径被跳过，好图补满四列。
	good := writePNG(t, dir, "good.png", poster(300, 450, palette[0]))
	img, err := Render([]string{missing, corrupt, good}, Options{Reflection: true})
	if err != nil {
		t.Fatalf("混合输入: %v", err)
	}
	g := Layout{}.Geometry()
	for i := 0; i < 4; i++ {
		x, y := columnCenter(g, i)
		if c := rgbaAt(img, x, y); !isBright(c) {
			t.Fatalf("列 %d 未被好图补齐: %+v", i, c)
		}
	}
}

// ---------- 5. 像素级断言 ----------

func TestPixelsLayout(t *testing.T) {
	paths := posters(t, 4)
	img, err := Render(paths, Options{Reflection: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	g := Layout{}.Geometry()

	// 主体顶部 y=4、x=8 是海报像素。
	if c := rgbaAt(img, g.X[0], g.BodyY); !isBright(c) {
		t.Fatalf("(%d,%d) 应为海报像素: %+v", g.X[0], g.BodyY, c)
	}
	// x=0..7 纯黑。
	for x := 0; x < g.Padding; x++ {
		if c := rgbaAt(img, x, g.BodyY+10); !isBlack(c) {
			t.Fatalf("左 padding x=%d 应为黑: %+v", x, c)
		}
	}
	// 列间 8px 间隙纯黑。
	for _, gap := range []int{g.X[0] + g.Slice, g.X[1] + g.Slice, g.X[2] + g.Slice} {
		for x := gap; x < gap+g.Padding; x++ {
			if c := rgbaAt(img, x, g.BodyY+10); !isBlack(c) {
				t.Fatalf("列间隙 x=%d 应为黑: %+v", x, c)
			}
		}
	}
	// 顶部 spacing 纯黑。
	for y := 0; y < g.BodyY; y++ {
		if c := rgbaAt(img, 300, y); !isBlack(c) {
			t.Fatalf("顶部 y=%d 应为黑: %+v", y, c)
		}
	}
	// 主体最后一行 y=255 是海报，y=256..259 是黑。
	if c := rgbaAt(img, g.X[0]+g.Slice/2, g.BodyY+g.BodyHeight-1); !isBright(c) {
		t.Fatalf("主体最后一行应为海报: %+v", c)
	}
	for y := g.BodyY + g.BodyHeight; y < g.ReflectionY; y++ {
		if c := rgbaAt(img, g.X[0]+g.Slice/2, y); !isBlack(c) {
			t.Fatalf("主体与倒影之间 y=%d 应为黑: %+v", y, c)
		}
	}
	// 倒影起点 y=260 仍有内容，末端 y=359 已被 alpha=255 压成纯黑。
	if c := rgbaAt(img, g.X[0]+g.Slice/2, g.ReflectionY); !isBright(c) {
		t.Fatalf("倒影首行应为可见像素: %+v", c)
	}
	if c := rgbaAt(img, g.X[0]+g.Slice/2, 359); !isBlack(c) {
		t.Fatalf("倒影末行应为纯黑: %+v", c)
	}
	// 倒影应当越往下越暗。
	top := rgbaAt(img, g.X[0]+g.Slice/2, g.ReflectionY+2)
	mid := rgbaAt(img, g.X[0]+g.Slice/2, g.ReflectionY+50)
	if sum(top) <= sum(mid) {
		t.Fatalf("倒影应自上而下变暗: top=%v mid=%v", top, mid)
	}
}

func sum(c color.RGBA) int { return int(c.R) + int(c.G) + int(c.B) }

func TestReflectionDisabled(t *testing.T) {
	img, err := Render(posters(t, 4), Options{Reflection: false})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	g := Layout{}.Geometry()
	for y := g.ReflectionY; y < 360; y++ {
		if c := rgbaAt(img, g.X[0]+g.Slice/2, y); !isBlack(c) {
			t.Fatalf("Reflection=false 时 y=%d 应为黑: %+v", y, c)
		}
	}
}

// ---------- 6. 确定性与幂等 ----------

func TestDeterministic(t *testing.T) {
	paths := posters(t, 6)
	first, err := RenderWebP(paths, Options{Reflection: true}, 90)
	if err != nil {
		t.Fatalf("RenderWebP: %v", err)
	}
	second, err := RenderWebP(paths, Options{Reflection: true}, 90)
	if err != nil {
		t.Fatalf("RenderWebP 第二次: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("两次输出不一致: %d vs %d 字节", len(first), len(second))
	}
	if len(first) == 0 {
		t.Fatal("输出为空")
	}

	// 默认画质分支（quality<=0 → 90）应与显式 90 等价。
	zero, err := RenderWebP(paths, Options{Reflection: true}, 0)
	if err != nil {
		t.Fatalf("RenderWebP quality=0: %v", err)
	}
	if !bytes.Equal(zero, first) {
		t.Fatal("quality<=0 未回落到 90")
	}
}

// ---------- 7. iWidth < iSlice 的降级分支 ----------

func TestNarrowImageSkipsReflection(t *testing.T) {
	dir := t.TempDir()
	// 10×1000 → iWidth = 252*10/1000 = 2 < 150，subset 越界。
	narrow := writePNG(t, dir, "narrow.png", poster(10, 1000, palette[0]))
	normal := writePNG(t, dir, "normal.png", poster(300, 450, palette[1]))

	img, err := Render([]string{narrow, normal}, Options{Reflection: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	g := Layout{}.Geometry()

	// 第 0 列（窄图）：主体只有 2px 宽，下方没有任何倒影。
	if c := rgbaAt(img, g.X[0], g.BodyY); !isBright(c) {
		t.Fatalf("窄图主体应可见: %+v", c)
	}
	for y := g.ReflectionY; y < 360; y++ {
		if c := rgbaAt(img, g.X[0], y); !isBlack(c) {
			t.Fatalf("窄图列在 y=%d 不应有倒影: %+v", y, c)
		}
	}
	// 第 1 列（正常图）：倒影存在。
	if c := rgbaAt(img, g.X[1]+g.Slice/2, g.ReflectionY); !isBright(c) {
		t.Fatalf("正常列应有倒影: %+v", c)
	}
}

// ---------- 输入不被修改 ----------

func TestInputsUntouched(t *testing.T) {
	paths := posters(t, 3)
	before := make([][]byte, len(paths))
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = data
	}
	if _, err := RenderWebP(paths, Options{Reflection: true}, 80); err != nil {
		t.Fatalf("RenderWebP: %v", err)
	}
	for i, p := range paths {
		after, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before[i], after) {
			t.Fatalf("%s 被修改了", p)
		}
	}
}

// 确认 640×360 的默认常量与源码一致（防止有人"顺手"改系数）。
func TestSourceConstants(t *testing.T) {
	if defaultWidth != 640 || defaultHeight != 360 || defaultColumns != 4 {
		t.Fatal("默认画布常量被改动")
	}
	if math.Abs(sliceRatio-0.23475) > 1e-12 ||
		math.Abs(bodyRatio-0.70) > 1e-12 ||
		math.Abs(paddingRatio-0.0125) > 1e-12 ||
		math.Abs(spacingRatio-1.0/90) > 1e-12 {
		t.Fatal("几何系数被改动")
	}
}
