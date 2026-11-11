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
// Canonical regular fanartN.webp and legacy fanartN.jpg files are assets;
// WebP wins for duplicate indexes. Links and nested files are not followed.
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
	numbered := make(map[int]imageSnapshot)
	var numbers []int
	for _, entry := range entries {
		name := entry.Name()
		ext := filepath.Ext(name)
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, "fanart") || (ext != ".jpg" && ext != ".webp") {
			continue
		}
		numberText := strings.TrimSuffix(strings.TrimPrefix(name, "fanart"), ext)
		number, err := strconv.Atoi(numberText)
		if err != nil || number < 1 || strconv.Itoa(number) != numberText {
			continue
		}
		_, exists := numbered[number]
		if exists && ext == ".jpg" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !exists {
			numbers = append(numbers, number)
		}
		numbered[number] = imageSnapshot{
			Source: sourcePath, Kind: "extra_backdrop", Path: filepath.Join(dir, name),
			Present: true, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
		}
	}
	sort.Ints(numbers)
	result := make([]imageSnapshot, 0, len(numbers))
	for _, number := range numbers {
		result = append(result, numbered[number])
	}
	return result, nil
}
