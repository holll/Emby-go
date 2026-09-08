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

func EnsureWebP(dir, base string) (string, error) {
	webpPath := filepath.Join(dir, base+".webp")
	if _, err := os.Stat(webpPath); err == nil {
		return webpPath, nil
	}
	for _, ext := range []string{".jpg", ".jpeg", ".JPG", ".JPEG"} {
		sourcePath := filepath.Join(dir, base+ext)
		source, err := os.Open(sourcePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		tmpPath := webpPath + ".tmp"
		err = EncodeWebP(source, tmpPath)
		closeErr := source.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
		if closeErr != nil {
			_ = os.Remove(tmpPath)
			return "", closeErr
		}
		if err = os.Rename(tmpPath, webpPath); err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
		_ = os.Remove(sourcePath)
		return webpPath, nil
	}
	return "", nil
}

// EnsurePoster 生成主海报 poster.webp。Emby 兼容：目录里可能只有 folder.jpg/cover.jpg/default.jpg，
// 按 poster → folder → cover → default 的顺序找源图/已生成的 webp，转成 poster.webp。
func EnsurePoster(dir string) (string, error) {
	for _, base := range []string{"poster", "folder", "cover", "default"} {
		if webpPath := filepath.Join(dir, base+".webp"); fileExists(webpPath) {
			return webpPath, nil
		}
		for _, ext := range []string{".jpg", ".jpeg", ".JPG", ".JPEG"} {
			sourcePath := filepath.Join(dir, base+ext)
			if !fileExists(sourcePath) {
				continue
			}
			// 只有 poster 本名产物固定叫 poster.webp；其它候选源也转成 poster.webp 统一。
			out := filepath.Join(dir, "poster.webp")
			if base != "poster" {
				source, err := os.Open(sourcePath)
				if err != nil {
					return "", err
				}
				tmpPath := out + ".tmp"
				err = EncodeWebP(source, tmpPath)
				closeErr := source.Close()
				if err != nil {
					_ = os.Remove(tmpPath)
					return "", err
				}
				if closeErr != nil {
					_ = os.Remove(tmpPath)
					return "", closeErr
				}
				if err = os.Rename(tmpPath, out); err != nil {
					_ = os.Remove(tmpPath)
					return "", err
				}
				_ = os.Remove(sourcePath)
				return out, nil
			}
			return EnsureWebP(dir, base)
		}
	}
	return "", nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
