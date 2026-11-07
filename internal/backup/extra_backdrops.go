package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// copyExtraBackdrops copies scanner-recognized files, preserving library-relative paths.
// Walk uses Lstat: linked directories and files are not part of this disk layout.
func copyExtraBackdrops(libs []backupLibrary, root string) (int, error) {
	count := 0
	for _, lib := range libs {
		info, err := os.Lstat(lib.path)
		if errors.Is(err, os.ErrNotExist) {
			continue // Offline/removed libraries follow the existing backup convention.
		}
		if err != nil {
			return 0, fmt.Errorf("backup: inspect extra backdrop library: %w", err)
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.Walk(lib.path, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !info.Mode().IsRegular() || filepath.Base(filepath.Dir(path)) != "extrafanart" {
				return nil
			}
			text, ok := strings.CutPrefix(info.Name(), "fanart")
			if !ok {
				return nil
			}
			text, ok = strings.CutSuffix(text, ".jpg")
			if !ok {
				return nil
			}
			n, err := strconv.Atoi(text)
			if err != nil || n <= 0 || strconv.Itoa(n) != text {
				return nil
			}
			rel, err := filepath.Rel(lib.path, path)
			if err != nil {
				return err
			}
			dst := filepath.Join(root, fmt.Sprintf("lib-%d", lib.id), rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := copyFile(path, dst); err != nil {
				return err
			}
			count++
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("backup: copy extra backdrops: %w", err)
		}
	}
	return count, nil
}
