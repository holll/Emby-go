// Package collage 复刻 Emby 历史 Skia StripCollageBuilder.BuildThumbCollageBitmap
// 的横版拼贴：四列等比缩放、居中裁剪、底部渐变倒影，画布黑底。
//
// 只实现 BuildThumbCollage；BuildSquareCollage（2×2 网格）与 BuildPosterCollage
// （源码里是 @todo 空实现）刻意不实现。
package collage

import (
	"errors"
	"image"
	"image/draw"
	"math"

	"emby-go/internal/imageutil"
)

// ErrNoImages 表示给定的路径里没有任何一张可解码的图片。
var ErrNoImages = errors.New("collage: no decodable images")

// Emby 历史源码的默认画布与槽位数。
const (
	defaultWidth   = 640
	defaultHeight  = 360
	defaultColumns = 4
)

// 源码里的几何系数，原样搬运，勿改成"更好看"的数值。
const (
	sliceRatio   = 0.23475
	bodyRatio    = 0.70
	paddingRatio = 0.0125
	spacingRatio = 0.01111111111111111111111111111111
)

// Layout 是拼贴几何；零值字段回落到 Emby 历史常量（640×360、4 个槽位）。
type Layout struct {
	Width   int
	Height  int
	Columns int
}

// Geometry 是从 Layout 推导出的实际像素几何，供测试与文档引用。
type Geometry struct {
	Slice            int   // 单列宽度 iSlice
	BodyHeight       int   // 海报主体高度 iHeight
	Padding          int   // 左右留距
	Spacing          int   // 顶部 / 主体与倒影之间的间距
	ReflectionHeight int   // 画布内可见的倒影高度
	BodyY            int   // 主体顶部 y
	ReflectionY      int   // 倒影顶部 y
	X                []int // 每个槽位左边界
}

// normalize 把零值字段补成 Emby 历史常量。
func (l Layout) normalize() Layout {
	if l.Width <= 0 {
		l.Width = defaultWidth
	}
	if l.Height <= 0 {
		l.Height = defaultHeight
	}
	if l.Columns <= 0 {
		l.Columns = defaultColumns
	}
	return l
}

// Geometry 按 Emby 历史源码的取整规则推导像素几何。
func (l Layout) Geometry() Geometry {
	l = l.normalize()
	slice := csharpRound(float64(l.Width) * sliceRatio)
	body := csharpRound(float64(l.Height) * bodyRatio)
	padding := csharpRound(float64(l.Width) * paddingRatio)
	spacing := csharpRound(float64(l.Height) * spacingRatio)
	reflectionY := body + 2*spacing

	x := make([]int, l.Columns)
	for i := range x {
		x[i] = padding*(i+1) + slice*i
	}

	return Geometry{
		Slice:            slice,
		BodyHeight:       body,
		Padding:          padding,
		Spacing:          spacing,
		ReflectionHeight: l.Height - reflectionY,
		BodyY:            spacing,
		ReflectionY:      reflectionY,
		X:                x,
	}
}

// csharpRound 复刻 .NET Convert.ToInt32(double) 的取整语义：Math.Round，
// 即四舍六入五取偶（banker's rounding）。int(v+0.5) 在中点上是错的。
func csharpRound(v float64) int {
	return int(math.RoundToEven(v))
}

// Options 是渲染参数。
type Options struct {
	Layout
	Reflection bool // 是否绘制倒影；false 时只画主体（源码行为为 true）
}

// Render 按 Emby 历史 BuildThumbCollage 的几何拼贴。paths 顺序即取图顺序，
// 解码失败的图片会被跳过并环形回绕。全部不可用时返回 ErrNoImages。
func Render(paths []string, opts Options) (*image.RGBA, error) {
	layout := opts.Layout.normalize()
	g := layout.Geometry()

	canvas := image.NewRGBA(image.Rect(0, 0, layout.Width, layout.Height))
	draw.Draw(canvas, canvas.Rect, image.Black, image.Point{}, draw.Src)

	decoded := 0
	imageIndex := 0
	for i := 0; i < layout.Columns; i++ {
		var src image.Image
		src, imageIndex = nextValidImage(paths, imageIndex)
		if src == nil {
			continue
		}

		decoded++
		// 尺寸异常（宽/高 <= 0、像素数超限）已在 decodeOriented 这个边界挡掉，
		// 所以这里的 Dx/Dy 一定为正。
		b := src.Bounds()
		iWidth := g.BodyHeight * b.Dx() / b.Dy() // C# 里全是 int：整数除法
		// iWidth < 1：源码会建出 0 宽的位图，这里按不可用跳过。
		// iWidth*BodyHeight 超上限：5000 万像素的极端长条图（如 50000000×1）会
		// 算出一个上千万宽的中间位图，直接分配就是 OOM —— 同一道解压炸弹防线。
		if iWidth < 1 || int64(iWidth)*int64(g.BodyHeight) > maxDecodePixels {
			continue
		}

		resized := resizeLanczos3(src, iWidth, g.BodyHeight)
		if resized == nil {
			continue
		}

		x := g.X[i]
		ix := (iWidth - g.Slice) / 2
		if ix < 0 {
			ix = -ix
		}

		if ix+g.Slice > iWidth {
			// 对应源码里 SKImage.Subset 越界返回 null：只画缩放图，且不画倒影。
			draw.Draw(canvas, image.Rect(x, g.BodyY, x+iWidth, g.BodyY+g.BodyHeight),
				resized, image.Point{}, draw.Over)
			continue
		}

		draw.Draw(canvas, image.Rect(x, g.BodyY, x+g.Slice, g.BodyY+g.BodyHeight),
			resized, image.Pt(ix, 0), draw.Over)

		if opts.Reflection {
			drawReflection(canvas, resized, ix, x, g)
		}
	}

	if decoded == 0 {
		return nil, ErrNoImages
	}
	return canvas, nil
}

// RenderWebP 渲染并编码为 WebP（quality<=0 时用 90，与源码 Encode(...,90) 一致）。
func RenderWebP(paths []string, opts Options, quality int) ([]byte, error) {
	if quality <= 0 {
		quality = 90
	}
	img, err := Render(paths, opts)
	if err != nil {
		return nil, err
	}
	return imageutil.EncodeWebPBuffer(img, quality)
}

// drawReflection 在主体下方画倒影：裁剪块缩到一半高、垂直翻转，再按源码的
// 四段线性渐变压暗，只画画布内可见的那几行（源码用 ClipRect 限定）。
func drawReflection(canvas *image.RGBA, body *image.RGBA, ix, x int, g Geometry) {
	half := g.BodyHeight / 2
	visible := g.ReflectionHeight
	if half <= 0 || visible <= 0 {
		return
	}

	cropped := body.SubImage(image.Rect(ix, 0, ix+g.Slice, g.BodyHeight))
	refl := resizeLanczos3(cropped, g.Slice, half)
	if refl == nil {
		return
	}
	flipVertical(refl)
	blendFaded(canvas, refl, x, g.ReflectionY, min(visible, half))
}

// blendFaded 把 src 的前 rows 行以渐变黑 SrcOver 合成到 dst 的 (x,y) 起点，
// 超出画布的部分由 draw 的裁剪语义丢弃（对应源码的 ClipRect）。
//
// 源码是 SKShader.CreateLinearGradient((0,0)→(0,remainingHeight), 四个纯黑
// 颜色 {128,208,240,255}) 配 SrcOver 画到翻转图上，再把翻转图 SrcOver 到
// 黑底画布。纯黑 SrcOver 等价于把目标乘 (1-a)；由于主体来自不透明画布
// （alpha 恒为 255），两次合成合起来就是 out = src*(1-a)，alpha 恒为 255。
// 这正好等于用 alpha = 255*(1-a) 的蒙版做一次 SrcOver，所以用 DrawMask。
//
// 近似说明：行内 alpha 用像素中心 (y+0.5)/rows 在 4 个停靠点间线性插值，
// 而 Skia 逐像素算浮点 alpha 并做子像素抗锯齿混合；两者行内一致，边界行
// 可能差 1 个色阶，属可接受的近似。
func blendFaded(dst, src *image.RGBA, x, y, rows int) {
	w := src.Rect.Dx()
	mask := image.NewAlpha(image.Rect(0, 0, w, rows))
	for j := 0; j < rows; j++ {
		m := uint8(255 - int(math.Round(gradientAlpha((float64(j)+0.5)/float64(rows)))))
		row := mask.Pix[j*mask.Stride:]
		for i := 0; i < w; i++ {
			row[i] = m
		}
	}
	draw.DrawMask(dst, image.Rect(x, y, x+w, y+rows), src, image.Point{}, mask, image.Point{}, draw.Over)
}

// gradientAlpha 是 4 个等距停靠点 {128,208,240,255} 在 t∈[0,1] 上的分段线性插值。
func gradientAlpha(t float64) float64 {
	const (
		a0 = 128.0
		a1 = 208.0
		a2 = 240.0
		a3 = 255.0
	)
	if t <= 0 {
		return a0
	}
	if t >= 1 {
		return a3
	}
	seg := t * 3
	switch {
	case seg <= 1:
		return a0 + (a1-a0)*seg
	case seg <= 2:
		return a1 + (a2-a1)*(seg-1)
	default:
		return a2 + (a3-a2)*(seg-2)
	}
}

// flipVertical 原地上下翻转。
func flipVertical(img *image.RGBA) {
	w := img.Rect.Dx()
	h := img.Rect.Dy()
	tmp := make([]byte, w*4)
	for y := 0; y < h/2; y++ {
		top := img.Pix[y*img.Stride:][:w*4]
		bottom := img.Pix[(h-1-y)*img.Stride:][:w*4]
		copy(tmp, top)
		copy(top, bottom)
		copy(bottom, tmp)
	}
}

// nextValidImage 从 currentIndex 开始环形遍历 paths，返回第一张可解码的图与
// 推进后的索引；全部不可用返回 (nil, 索引)。
//
// 源码用 Dictionary 记录试过的下标，这里直接最多尝试 len(paths) 次：环形遍历
// 下每个下标只会被访问一次，效果等价。
func nextValidImage(paths []string, currentIndex int) (image.Image, int) {
	for tried := 0; tried < len(paths); tried++ {
		if currentIndex >= len(paths) {
			currentIndex = 0
		}
		img, err := decodeOriented(paths[currentIndex])
		currentIndex++
		if err == nil {
			return img, currentIndex
		}
	}
	return nil, currentIndex
}
