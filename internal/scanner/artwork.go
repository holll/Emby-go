package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// extraBackdropSnapshots inventories the approved on-disk layout in numeric order.
// Only canonical regular fanartN.jpg files are assets; links and nested files are not.
func extraBackdropSnapshots(dir, sourcePath string) ([]imageSnapshot, error) {
	dir, err := filepath.Abs(filepath.Join(dir, "extrafanart"))
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type numberedImage struct {
		number int
		image  imageSnapshot
	}
	var numbered []numberedImage
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, "fanart") || !strings.HasSuffix(name, ".jpg") {
			continue
		}
		numberText := strings.TrimSuffix(strings.TrimPrefix(name, "fanart"), ".jpg")
		number, err := strconv.Atoi(numberText)
		if err != nil || number < 1 || strconv.Itoa(number) != numberText {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		numbered = append(numbered, numberedImage{number, imageSnapshot{
			Source: sourcePath, Kind: "extra_backdrop", Path: filepath.Join(dir, name),
			Present: true, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
		}})
	}
	sort.Slice(numbered, func(i, j int) bool { return numbered[i].number < numbered[j].number })
	result := make([]imageSnapshot, 0, len(numbered))
	for _, entry := range numbered {
		result = append(result, entry.image)
	}
	return result, nil
}
