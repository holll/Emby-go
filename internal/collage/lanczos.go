package collage

import (
	"image"
	"image/draw"
	"math"
)

// lanczosA 是 Lanczos 窗口半径：a=3 即 Lanczos3。
const lanczosA = 3.0

// resizeLanczos3 把 src 重采样到 w×h（放大/缩小都支持），边界按 clamp 处理。
//
// 可分离两趟：先水平后垂直。缩小的时候核宽按缩放比放大（filterScale），
// 这是抗混叠必须的；否则缩小只是抽样，会出摩尔纹。每行/每列权重在采样前
// 归一化，所以常量色块不会被振铃染出黑边。
//
// w 或 h <= 0 返回 nil（调用方已挡）。返回的图 Rect 从原点开始。
func resizeLanczos3(src image.Image, w, h int) *image.RGBA {
	if w <= 0 || h <= 0 {
		return nil
	}
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}

	// 统一成原点对齐的 *image.RGBA：draw.Draw 走标准库快速路径（YCbCr/NRGBA
	// 都有专用转换），之后两趟滤波直接读字节，比逐像素 At() 快一个量级。
	flat := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(flat, flat.Rect, src, sb.Min, draw.Src)

	// 第一趟：水平，得到 w×sh 的浮点（预乘、0..255 标度）中间结果。
	hbuf := make([]float32, w*sh*4)
	cols := lanczosTaps(w, sw)
	for y := 0; y < sh; y++ {
		row := flat.Pix[y*flat.Stride:]
		out := hbuf[y*w*4:]
		for x := 0; x < w; x++ {
			t := &cols[x]
			var r, g, b, a float32
			for k, weight := range t.weights {
				fw := float32(weight)
				p := row[(t.start+k)*4:]
				r += float32(p[0]) * fw
				g += float32(p[1]) * fw
				b += float32(p[2]) * fw
				a += float32(p[3]) * fw
			}
			o := out[x*4:]
			o[0], o[1], o[2], o[3] = r, g, b, a
		}
	}
	flat = nil // 让第一趟结束就能回收整张源图

	// 第二趟：垂直，同时把预乘浮点落成 8 位预乘 RGBA。
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	rows := lanczosTaps(h, sh)
	for y := 0; y < h; y++ {
		t := &rows[y]
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			var r, g, b, a float32
			for k, weight := range t.weights {
				fw := float32(weight)
				o := hbuf[((t.start+k)*w+x)*4:]
				r += o[0] * fw
				g += o[1] * fw
				b += o[2] * fw
				a += o[3] * fw
			}
			storePremul(out[x*4:], r, g, b, a)
		}
	}
	return dst
}

// storePremul 把预乘浮点（0..255 标度）写成 8 位预乘 RGBA —— image.RGBA 存的
// 就是预乘值，所以这里不做还原，直接截断。
func storePremul(dst []byte, r, g, b, a float32) {
	dst[0] = clamp8(r)
	dst[1] = clamp8(g)
	dst[2] = clamp8(b)
	dst[3] = clamp8(a)
}

// clamp8 把浮点截到 0..255 并四舍五入。Lanczos 会过冲（振铃），负值必须截零。
func clamp8(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// taps 是一组滤波抽头：src 上从 start 开始的连续 len(weights) 个样本及权重。
type taps struct {
	start   int
	weights []float64
}

// lanczosTaps 为每个目标下标算出源上的抽头与归一化权重。
func lanczosTaps(dstSize, srcSize int) []taps {
	scale := float64(srcSize) / float64(dstSize) // 每个目标像素覆盖多少源像素
	filterScale := scale
	if filterScale < 1 {
		filterScale = 1 // 放大时不加宽核，避免过度平滑
	}
	support := lanczosA * filterScale

	out := make([]taps, dstSize)
	for i := 0; i < dstSize; i++ {
		center := (float64(i)+0.5)*scale - 0.5 // 目标像素中心在源坐标上的位置
		lo := int(math.Ceil(center - support))
		hi := int(math.Floor(center + support))
		if lo < 0 {
			lo = 0
		}
		if hi > srcSize-1 {
			hi = srcSize - 1
		}
		weights := make([]float64, 0, hi-lo+1)
		sum := 0.0
		for j := lo; j <= hi; j++ {
			w := lanczos3((float64(j) - center) / filterScale)
			weights = append(weights, w)
			sum += w
		}
		// 归一化：既保证常量色块原样通过，也避免 clamp 后边缘变暗。
		for k := range weights {
			weights[k] /= sum
		}
		out[i] = taps{start: lo, weights: weights}
	}
	return out
}

// lanczos3 是 a=3 的 Lanczos 核：sinc(x)·sinc(x/3)，|x|>=3 时为 0。
func lanczos3(x float64) float64 {
	if x == 0 {
		return 1
	}
	if x < 0 {
		x = -x
	}
	if x >= lanczosA {
		return 0
	}
	px := math.Pi * x
	return lanczosA * math.Sin(px) * math.Sin(px/lanczosA) / (px * px)
}
