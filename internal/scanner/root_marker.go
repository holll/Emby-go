package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrRootMarker indicates invalid configuration or an unhealthy root marker.
var ErrRootMarker = errors.New("scan root marker validation failed")

// ValidateRootMarkerOptions validates the configuration without accessing disk.
func ValidateRootMarkerOptions(opts ScanOptions) error {
	if opts.RootMarkerPath == "" && opts.RootMarkerSHA256 == "" {
		return nil
	}
	if opts.RootMarkerPath == "" || opts.RootMarkerSHA256 == "" {
		return fmt.Errorf("%w: path and SHA256 must be supplied together", ErrRootMarker)
	}
	path := opts.RootMarkerPath
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return fmt.Errorf("%w: marker path must be relative", ErrRootMarker)
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return fmt.Errorf("%w: parent traversal is forbidden", ErrRootMarker)
		}
	}
	clean := filepath.Clean(path)
	if clean == "." {
		return fmt.Errorf("%w: marker must name a file", ErrRootMarker)
	}
	digest, err := hex.DecodeString(opts.RootMarkerSHA256)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("%w: SHA256 must contain 64 hexadecimal characters", ErrRootMarker)
	}
	return nil
}

func validateRootMarker(root string, opts ScanOptions) error {
	if err := ValidateRootMarkerOptions(opts); err != nil {
		return err
	}
	if opts.RootMarkerPath == "" {
		return nil
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRootMarker, err)
	}
	rootReal, err = filepath.Abs(rootReal)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRootMarker, err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(rootReal, opts.RootMarkerPath))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRootMarker, err)
	}
	rel, err := filepath.Rel(rootReal, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: marker escapes library root", ErrRootMarker)
	}
	before, err := os.Stat(target)
	if err != nil || !before.Mode().IsRegular() {
		return fmt.Errorf("%w: marker is not a regular file", ErrRootMarker)
	}
	file, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRootMarker, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return fmt.Errorf("%w: marker is not a regular file", ErrRootMarker)
	}
	const limit = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRootMarker, err)
	}
	if len(data) > limit {
		return fmt.Errorf("%w: marker exceeds 64 KiB", ErrRootMarker)
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), opts.RootMarkerSHA256) {
		return fmt.Errorf("%w: digest mismatch", ErrRootMarker)
	}
	return nil
}
