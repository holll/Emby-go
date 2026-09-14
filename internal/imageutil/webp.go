package imageutil

import (
	"bytes"
	"image"
	"image/draw"
	"io"
	"math"
	"os"
	"path/filepath"

	_ "image/jpeg"
	_ "image/png"

	"github.com/deepteams/webp"
)

// defaultQuality 未指定画质时的 webp 编码质量（与上传转码保持一致）。
const defaultQuality = 82

// EncodeWebP 把读取到的图片编码为 webp 写入 destination（destination 为最终路径）。
// 仅用于管理端“上传即转 webp”这类显式操作；扫描不再做格式转换。
func EncodeWebP(src io.Reader, destination string) error {
	img, _, err := image.Decode(src)
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
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	scaled := Downscale(img, maxWidth, maxHeight)
	if scaled == img {
		return nil, nil
	}
	return EncodeWebPBuffer(scaled, quality)
}

// Downscale 按盒子平均把图片缩小到不超过 maxWidth×maxHeight（宽高任一 <=0 表示该维度不限）。
// 只缩小不放大：不需要缩小时原样返回。盒子平均在缩小时比双线性更少摩尔纹，
// 且一次遍历即可完成，适合做缩略图。
func Downscale(src image.Image, maxWidth, maxHeight int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return src
	}
	scale := 1.0
	if maxWidth > 0 && width > maxWidth {
		scale = math.Min(scale, float64(maxWidth)/float64(width))
	}
	if maxHeight > 0 && height > maxHeight {
		scale = math.Min(scale, float64(maxHeight)/float64(height))
	}
	if scale >= 1 {
		return src
	}
	targetWidth := max(1, int(float64(width)*scale+0.5))
	targetHeight := max(1, int(float64(height)*scale+0.5))

	// 转成 RGBA 后直接按下标取像素：逐像素走 image.Image 接口在缩略图尺寸下也偏慢。
	source := toRGBA(src)
	destination := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		startY, endY := y*height/targetHeight, (y+1)*height/targetHeight
		if endY <= startY {
			endY = startY + 1
		}
		for x := 0; x < targetWidth; x++ {
			startX, endX := x*width/targetWidth, (x+1)*width/targetWidth
			if endX <= startX {
				endX = startX + 1
			}
			var red, green, blue, alpha, count uint64
			for sy := startY; sy < endY; sy++ {
				// toRGBA 已把原点归零，行下标就是 sy*Stride。
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
