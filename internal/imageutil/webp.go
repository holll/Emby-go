package imageutil

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	_ "image/jpeg"
	_ "image/png"

	"github.com/deepteams/webp"
)

// defaultQuality 未指定画质时的 webp 编码质量（与上传转码保持一致）。
const defaultQuality = 82

// maxDecodePixels 单张图解码后的像素上限（宽×高）。
//
// 体积上限拦不住「解压炸弹」：几百 KB 的 PNG 就能声明到数十亿像素，而
// image.Decode 会按 宽×高×4 一次性分配内存，几 MB 的下载瞬间撑成几十 GB。
// 所以必须先用只读头部的 DecodeConfig 拿到真实尺寸，超限直接拒绝。
const maxDecodePixels = 50_000_000 // 5000 万像素，约 8200×6100

// errImageTooLarge 表示图片声明的像素数超出 maxDecodePixels。
var errImageTooLarge = errors.New("imageutil: 图片尺寸超出上限")

// errDecodePanic 表示标准库解码器在畸形输入上 panic 了（历史版本里 png/jpeg
// 都出现过越界、除零这类崩溃）。它被当作普通解码失败处理：调用方回退原图。
var errDecodePanic = errors.New("imageutil: 解码图片时崩溃")

// decodeImage / decodeImageConfig 是 image.Decode / image.DecodeConfig 的
// 包级接缝。存在的唯一目的是让测试能换成会 panic 的实现，从而验证下面的
// recover 守卫真的生效（畸形图片并不保证每次都能触发标准库 panic）。
var (
	decodeImage       = image.Decode
	decodeImageConfig = image.DecodeConfig
)

// maxPooledRGBABytes 单个 RGBA 缓冲进入池的容量上限。超过就交给 GC：
// 4000×3000 的 RGBA 要 48 MB，常驻下来比每次重新分配更亏。
const maxPooledRGBABytes = 8 << 20

// maxStripBytes 一次转换的源图条带字节上限（1 MiB；1920 宽时约 136 行）。
// 条带越小越省峰值内存，越大 draw.Draw 的调用开销越少。
const maxStripBytes = 1 << 20

// rgbaPool 复用缩略图管线的 RGBA 中转缓冲（源图条带 + 缩放目标）。
// 池里的对象一律以「已归零的容量」形态存放，取用时由 resetRGBA 按本次
// 尺寸重设 Rect/Stride 并截断 Pix，所以不会把上一张图的像素带进下一张。
var rgbaPool = sync.Pool{
	New: func() any { return new(image.RGBA) },
}

// getRGBA 取一块干净的 RGBA 画布：Rect=(0,0,width,height)、Stride=4*width、
// len(Pix) == 4*Stride*height。归还用 putRGBA。
func getRGBA(width, height int) *image.RGBA {
	img, _ := rgbaPool.Get().(*image.RGBA)
	return resetRGBA(img, width, height)
}

// resetRGBA 把缓冲整理成 width×height 的画布。
//
// image.RGBA 的不变式是 len(Pix) >= 4*Stride*Rect.Dy()，而池里取出的缓冲是
// 上一次请求的形状，Stride 可能 != 4*width、Pix 可能更长或更短。所以必须
// 显式重算 Stride、把 Pix 截到恰好 4*Stride*height，再改 Rect —— 只改 Rect
// 不改 Stride 会让行下标错位，只靠截断不改 Stride 会让上一张图的残留像素
// 从更宽的旧行里渗进本张图。
func resetRGBA(img *image.RGBA, width, height int) *image.RGBA {
	if img == nil {
		img = new(image.RGBA)
	}
	stride := 4 * width
	size := stride * height
	if cap(img.Pix) < size {
		img.Pix = make([]byte, size)
	}
	img.Pix = img.Pix[:size]
	img.Stride = stride
	img.Rect = image.Rect(0, 0, width, height)
	return img
}

// putRGBA 把缓冲归还池里；过大的缓冲直接丢弃，避免大图把内存钉死在池中。
func putRGBA(img *image.RGBA) {
	if img == nil || cap(img.Pix) > maxPooledRGBABytes {
		return
	}
	img.Rect = image.Rectangle{}
	img.Stride = 0
	img.Pix = img.Pix[:0]
	rgbaPool.Put(img)
}

// decodeLimited 先按图片头校验尺寸，再回到流开头做真正的解码。
// 调用方的上限只管字节数，这一层才是防解压炸弹的那道闸。
//
// recover 的边界：只包「读头 + 解码」这两步，也就是唯一由不可信输入驱动的
// 环节。Downscale/编码是自家代码，那里的 panic 属于真 bug，不该被原谅成
// 「图片坏了」（media.go 的 leader defer 会保证 flight 不因此泄漏）。
func decodeLimited(rs io.ReadSeeker) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			img = nil
			err = fmt.Errorf("%w: %v", errDecodePanic, r)
		}
	}()
	config, _, err := decodeImageConfig(rs)
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("imageutil: 图片尺寸非法 (%dx%d)", config.Width, config.Height)
	}
	if int64(config.Width)*int64(config.Height) > maxDecodePixels {
		return nil, fmt.Errorf("%w: %dx%d", errImageTooLarge, config.Width, config.Height)
	}
	// DecodeConfig 已消费掉文件头，解码前必须复位，否则会从头解析失败。
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err = decodeImage(rs)
	return img, err
}

// EncodeWebP 把读取到的图片编码为 webp 写入 destination（destination 为最终路径）。
// 仅用于管理端“上传即转 webp”这类显式操作；扫描不再做格式转换。
// src 需可定位：解码前要先读头校验尺寸，再复位到流开头。
func EncodeWebP(src io.ReadSeeker, destination string) error {
	img, err := decodeLimited(src)
	if err != nil {
		return err
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	if err = webp.Encode(file, img, &webp.EncoderOptions{Quality: defaultQuality, Method: 4}); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// EncodeWebPTo encodes into a caller-owned writer with the same decode limits
// and quality as EncodeWebP. It neither closes nor syncs the writer.
func EncodeWebPTo(src io.ReadSeeker, dst io.Writer) error {
	img, err := decodeLimited(src)
	if err != nil {
		return err
	}
	return webp.Encode(dst, img, &webp.EncoderOptions{Quality: defaultQuality, Method: 4})
}

// EncodeWebPBuffer 把图片编码为 webp 字节，供缩略图这类「不落盘中转」的场景使用。
// quality <=0 时用默认画质。
func EncodeWebPBuffer(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = defaultQuality
	}
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.EncoderOptions{Quality: float32(quality), Method: 4}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Thumbnail 打开 srcPath，按 maxWidth/maxHeight 缩小并按 quality 编码为 webp 字节。
// 目标尺寸不小于原图时返回 (nil, nil)：调用方直接发原图，不做无意义的转码。
func Thumbnail(srcPath string, maxWidth, maxHeight, quality int) ([]byte, error) {
	file, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// webp 由本包引入的 github.com/deepteams/webp 注册进 image 包，jpeg/png 见上面的空白导入。
	img, err := decodeLimited(file)
	if err != nil {
		return nil, err
	}
	scaled, release, ok := scalePooled(img, maxWidth, maxHeight)
	if !ok {
		return nil, nil
	}
	// 编码把像素读完之后才能归还缓冲：release 放在 defer 里，编码失败也不漏。
	defer release()
	return EncodeWebPBuffer(scaled, quality)
}

// Downscale 按盒子平均把图片缩小到不超过 maxWidth×maxHeight（宽高任一 <=0 表示该维度不限）。
// 只缩小不放大：不需要缩小时原样返回。盒子平均在缩小时比双线性更少摩尔纹，
// 且一次遍历即可完成，适合做缩略图。
//
// 这是「调用方持有结果」的公共形式：源图要转换时分配一份全尺寸 RGBA、目标图
// 每次新分配。缩略图管线走 scalePooled，两者输出的像素完全一致。
func Downscale(src image.Image, maxWidth, maxHeight int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	targetWidth, targetHeight, ok := scaleTarget(width, height, maxWidth, maxHeight)
	if !ok {
		return src
	}
	// 转成 RGBA 后直接按下标取像素：逐像素走 image.Image 接口在缩略图尺寸下也偏慢。
	source := toRGBA(src)
	destination := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	boxScale(source, destination, width, height, targetWidth, targetHeight, 0, targetHeight, 0)
	return destination
}

// scaleTarget 按 maxWidth/maxHeight 算目标尺寸；不需要缩放（或源图尺寸非法）时
// ok=false，调用方原样使用源图。
func scaleTarget(width, height, maxWidth, maxHeight int) (targetWidth, targetHeight int, ok bool) {
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	scale := 1.0
	if maxWidth > 0 && width > maxWidth {
		scale = math.Min(scale, float64(maxWidth)/float64(width))
	}
	if maxHeight > 0 && height > maxHeight {
		scale = math.Min(scale, float64(maxHeight)/float64(height))
	}
	if scale >= 1 {
		return 0, 0, false
	}
	return max(1, int(float64(width)*scale+0.5)), max(1, int(float64(height)*scale+0.5)), true
}

// rowSpan 是第 y 个输出行覆盖的源行区间 [start, end)，列方向同理见 colSpan。
func rowSpan(y, height, targetHeight int) (int, int) {
	start, end := y*height/targetHeight, (y+1)*height/targetHeight
	if end <= start {
		end = start + 1
	}
	return start, end
}

func colSpan(x, width, targetWidth int) (int, int) {
	start, end := x*width/targetWidth, (x+1)*width/targetWidth
	if end <= start {
		end = start + 1
	}
	return start, end
}

// boxScale 把 src 的前 width×height 个像素按盒子平均缩进 dst 的 [yFrom, yTo) 行。
// srcRowBase 是 src 第 0 行对应的源图行号：源图按条带转换时，一条条带只覆盖
// 一段源行，条带内的行号要减去这个基准。dst 必须已按目标尺寸重置。
func boxScale(src, dst *image.RGBA, width, height, targetWidth, targetHeight, yFrom, yTo, srcRowBase int) {
	for y := yFrom; y < yTo; y++ {
		startY, endY := rowSpan(y, height, targetHeight)
		for x := range targetWidth {
			startX, endX := colSpan(x, width, targetWidth)
			var red, green, blue, alpha, count uint64
			for sy := startY; sy < endY; sy++ {
				row := src.Pix[(sy-srcRowBase)*src.Stride:]
				for sx := startX; sx < endX; sx++ {
					offset := sx * 4
					red += uint64(row[offset])
					green += uint64(row[offset+1])
					blue += uint64(row[offset+2])
					alpha += uint64(row[offset+3])
					count++
				}
			}
			offset := y*dst.Stride + x*4
			dst.Pix[offset] = uint8(red / count)
			dst.Pix[offset+1] = uint8(green / count)
			dst.Pix[offset+2] = uint8(blue / count)
			dst.Pix[offset+3] = uint8(alpha / count)
		}
	}
}

// scalePooled 是缩略图管线用的 Downscale：像素输出与 Downscale 逐字节一致，
// 但中间缓冲（非 RGBA 源的转换缓冲、目标图）全部来自 sync.Pool。
//
// 非 RGBA 源不做「先整张转 RGBA」的全尺寸复制，而是按 maxStripBytes 切成源图
// 条带：一次只把当前输出行需要的源行用 draw.Draw 转进条带再累加。draw.Draw
// 是逐点运算，条带切分只影响内存占用，不影响任何一个像素的值。
// 返回值 ok=false 表示无需缩放；release 必须调用（可能为 nil）。
func scalePooled(src image.Image, maxWidth, maxHeight int) (scaled *image.RGBA, release func(), ok bool) {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	targetWidth, targetHeight, ok := scaleTarget(width, height, maxWidth, maxHeight)
	if !ok {
		return nil, nil, false
	}
	destination := getRGBA(targetWidth, targetHeight)
	// 解码器给的 *image.RGBA 已经是紧凑的 RGBA 视图，直接用，不再复制一份。
	if rgba, isRGBA := src.(*image.RGBA); isRGBA {
		boxScale(rgba, destination, width, height, targetWidth, targetHeight, 0, targetHeight, 0)
		return destination, func() { putRGBA(destination) }, true
	}

	stripRows := max(1, maxStripBytes/(4*width))
	var strip *image.RGBA
	for y := 0; y < targetHeight; {
		firstRow, _ := rowSpan(y, height, targetHeight)
		last := y
		for last < targetHeight {
			_, end := rowSpan(last, height, targetHeight)
			if last > y && end-firstRow > stripRows {
				break
			}
			last++
		}
		_, bandEnd := rowSpan(last-1, height, targetHeight)
		strip = resetRGBA(strip, width, bandEnd-firstRow)
		// 条带首行对应源图的 firstRow 行：draw.Draw 逐点搬运，原点偏移量进 sp。
		draw.Draw(strip, strip.Bounds(), src, image.Point{X: bounds.Min.X, Y: bounds.Min.Y + firstRow}, draw.Src)
		boxScale(strip, destination, width, height, targetWidth, targetHeight, y, last, firstRow)
		y = last
	}
	return destination, func() { putRGBA(strip); putRGBA(destination) }, true
}

// toRGBA 返回 RGBA 像素视图：已是 *image.RGBA 时直接复用，否则转换一次。
func toRGBA(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok {
		return rgba
	}
	bounds := src.Bounds()
	destination := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(destination, destination.Bounds(), src, bounds.Min, draw.Src)
	return destination
}

// imageExts 目录里认可的目标图片扩展名。含 webp：兼容旧版本扫描已生成的
// poster.webp/fanart.webp/landscape.webp，重扫时不丢封面。
var imageExts = []string{".webp", ".jpg", ".jpeg", ".png", ".JPG", ".JPEG", ".PNG", ".WebP", ".WEBP"}

// findImage 返回 dir 下 base.<ext> 中第一个已存在的图片路径；不转换、不生成、不删除源文件。
func findImage(dir, base string) string {
	for _, ext := range imageExts {
		path := filepath.Join(dir, base+ext)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// FindPoster 返回目录中可作主海报的已有图片（poster→folder→cover→default，按此顺序）。
// 找不到返回空串。不做 webp 转换，也不删除任何源文件。
func FindPoster(dir string) string {
	for _, base := range []string{"poster", "folder", "cover", "default"} {
		if path := findImage(dir, base); path != "" {
			return path
		}
	}
	return ""
}

// FindMoviePoster prefers the private poster for one STRM over directory-wide
// artwork. Keeping the complete STRM basename avoids changing sibling movies.
func FindMoviePoster(dir, sourcePath string) string {
	if sourcePath != "" {
		path := filepath.Join(dir, filepath.Base(sourcePath)+".poster.jpg")
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return FindPoster(dir)
}

// FindImage 返回目录中指定命名（如 fanart / landscape）的已有图片；找不到返回空串。
func FindImage(dir, base string) string {
	return findImage(dir, base)
}

// AspectRatio 读取图片真实宽高比（宽/高）。jpg/jpeg/png/webp 均可；
// 读取失败返回 0，由调用方回退到按文件名猜测。
func AspectRatio(path string) float64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0
	}
	return float64(config.Width) / float64(config.Height)
}
