package imageutil

import (
	"image"
	"io"
	"os"
	"path/filepath"

	"github.com/deepteams/webp"
	_ "image/jpeg"
	_ "image/png"
)

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
	if err = webp.Encode(file, img, &webp.EncoderOptions{Quality: 82, Method: 4}); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
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
