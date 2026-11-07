package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/nfo"
)

func TestNFODigestSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.nfo")
	for _, size := range []int{nfo.MaxFileSize, nfo.MaxFileSize + 1} {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0644); err != nil {
			t.Fatal(err)
		}
		digest, err := nfoDigest(path)
		if size == nfo.MaxFileSize {
			if err != nil || len(digest) != 64 {
				t.Fatalf("exact boundary: digest=%q err=%v", digest, err)
			}
		} else if !errors.Is(err, nfo.ErrTooLarge) || digest != "" {
			t.Fatalf("oversized digest bypassed limit: digest=%q err=%v", digest, err)
		}
	}
}
