package collage

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"io"
	"os"
)

// maxDecodePixels 单张图解码后的像素上限（宽×高），与 imageutil 同口径。
//
// 体积拦不住解压炸弹：几百 KB 的 PNG 就能声明数十亿像素，而 image.Decode 会
// 按 宽×高×4 一次性分配。所以先只读文件头拿尺寸，超限直接判为不可用。
const maxDecodePixels = 50_000_000

// orientationExif 是 TIFF IFD0 里表示方向的 tag（0x0112）。
const orientationExif = 0x0112

// decodeOriented 打开 path，按 JPEG EXIF Orientation(1..8) 纠正方向后返回图片。
// 非 JPEG、无 EXIF、或解析失败时按原始方向返回，不报错。
//
// 这是不可信输入的边界（磁盘上的图片文件），所以尺寸校验与解码 panic 都在
// 这里挡掉；返回的图保证 Bounds().Dx()/Dy() 为正且像素数不超上限。
func decodeOriented(path string) (img image.Image, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// 只包「读头 + 解码」这两步：唯一由文件内容驱动的环节。畸形图片历史上
	// 让标准库 png/jpeg 解码器 panic 过，这里把它降级成普通的解码失败。
	defer func() {
		if r := recover(); r != nil {
			img = nil
			err = fmt.Errorf("collage: 解码 %s 时崩溃: %v", path, r)
		}
	}()

	orientation := exifOrientation(file) // 文件刚打开，流在起点
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("collage: %s 尺寸非法 (%dx%d)", path, config.Width, config.Height)
	}
	if int64(config.Width)*int64(config.Height) > maxDecodePixels {
		return nil, fmt.Errorf("collage: %s 尺寸超上限 (%dx%d)", path, config.Width, config.Height)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	return applyOrientation(src, orientation), nil
}

// applyOrientation 按 EXIF Orientation 1..8 变换图像；未知值原样返回。
// 5..8 会把宽高对调，因此返回的图可能是新分配的。
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}

	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dw, dh := sw, sh
	if orientation >= 5 {
		dw, dh = sh, sw
	}

	flat := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(flat, flat.Rect, src, sb.Min, draw.Src)

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < dw; x++ {
			sx, sy := orientationSource(orientation, x, y, sw, sh)
			in := flat.Pix[sy*flat.Stride+sx*4:]
			copy(out[x*4:x*4+4], in[:4])
		}
	}
	return dst
}

// orientationSource 给出目标像素 (x,y) 在源图上的对应位置。
// 取值遵循 EXIF 2.31 的 1..8 定义（镜像 + 旋转的组合）。
func orientationSource(orientation, x, y, sw, sh int) (int, int) {
	switch orientation {
	case 2: // 水平镜像
		return sw - 1 - x, y
	case 3: // 旋转 180°
		return sw - 1 - x, sh - 1 - y
	case 4: // 垂直镜像
		return x, sh - 1 - y
	case 5: // 主对角线镜像（转置）
		return y, x
	case 6: // 顺时针 90°
		return y, sh - 1 - x
	case 7: // 副对角线镜像
		return sw - 1 - y, sh - 1 - x
	case 8: // 逆时针 90°
		return sw - 1 - y, x
	}
	return x, y
}

// exifOrientation 从 JPEG 流的 APP1 "Exif\0\0" 段里取 IFD0 的 0x0112。
// 任何解析问题都返回 0（=按原始方向），调用方据此跳过旋转。
func exifOrientation(r io.Reader) int {
	br := bufio.NewReader(r)

	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return 0
	}

	for {
		// 段标记：0xFF 后面可以跟任意个 0xFF 填充。
		b, err := br.ReadByte()
		if err != nil {
			return 0
		}
		if b != 0xFF {
			return 0
		}
		for b == 0xFF {
			if b, err = br.ReadByte(); err != nil {
				return 0
			}
		}

		switch {
		case b == 0xD8 || b == 0x01 || (b >= 0xD0 && b <= 0xD7):
			continue // 无载荷的独立标记
		case b == 0xD9 || b == 0xDA:
			return 0 // EOI / SOS：图像数据开始，EXIF 只会在它之前
		}

		var size [2]byte
		if _, err := io.ReadFull(br, size[:]); err != nil {
			return 0
		}
		payload := int(binary.BigEndian.Uint16(size[:])) - 2
		if payload < 0 {
			return 0
		}

		if b != 0xE1 {
			if _, err := br.Discard(payload); err != nil {
				return 0
			}
			continue
		}

		// APP1 承载的可能是 Exif 也可能是 XMP，前缀不对就跳过这一段。
		segment := make([]byte, payload)
		if _, err := io.ReadFull(br, segment); err != nil {
			return 0
		}
		if len(segment) < 6 || string(segment[:6]) != "Exif\x00\x00" {
			continue
		}
		return tiffOrientation(segment[6:])
	}
}

// tiffOrientation 在 TIFF 头 + IFD0 里找 0x0112。结构不对一律返回 0。
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}

	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(tiff[2:]) != 42 {
		return 0
	}

	ifd := int(order.Uint32(tiff[4:]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 0
	}
	count := int(order.Uint16(tiff[ifd:]))
	entries := ifd + 2
	for i := 0; i < count; i++ {
		entry := entries + i*12
		if entry+12 > len(tiff) {
			return 0
		}
		if order.Uint16(tiff[entry:]) != orientationExif {
			continue
		}
		// 只认 SHORT(3) 且 count==1；值直接内联在 4 字节 value 字段的低两位。
		if order.Uint16(tiff[entry+2:]) != 3 || order.Uint32(tiff[entry+4:]) != 1 {
			return 0
		}
		return int(order.Uint16(tiff[entry+8:]))
	}
	return 0
}
