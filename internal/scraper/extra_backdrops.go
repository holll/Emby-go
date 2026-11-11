package scraper

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"emby-go/internal/imageutil"
	"emby-go/internal/metatube"
)

// downloadExtraBackdrop commits a selected still as real WebP, leaving any old target intact on failure.
func (s *Scraper) downloadExtraBackdrop(ctx context.Context, info metatube.MovieInfo, remoteURL, dest string, overwrite bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !overwrite {
		if stat, err := os.Stat(dest); err == nil && stat.Mode().IsRegular() && stat.Size() > 0 {
			return "", nil
		}
	}
	url := s.client.ImageURL("backdrop", info.Provider, info.ID, metatube.ImageOptions{
		RemoteURL: remoteURL, Quality: s.cfg.ImageQuality,
	})
	data, err := s.client.Download(ctx, url, maxImageBytes)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+"-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := imageutil.EncodeWebPTo(bytes.NewReader(data), file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("转 WebP 失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	release := lockImageCommit(filepath.Clean(key))
	defer release()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !overwrite {
		if stat, err := os.Stat(dest); err == nil && stat.Mode().IsRegular() && stat.Size() > 0 {
			return "", nil
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}
