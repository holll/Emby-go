package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanUnavailableRootPreservesUserData(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(media, "A.strm")
	if err := os.WriteFile(source, []byte("https://media.test/a.mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "A.nfo"), []byte("<movie><title>A</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, root)
	lib, err := s.AddLibrary("L", media)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err != nil {
		t.Fatal(err)
	}
	id, err := s.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite(id, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlayback(id, 123, 2, "2026-10-16T00:00:00Z", 123); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(media, media+"-offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(s, lib); err == nil {
		t.Fatal("根目录不可用时扫描必须失败")
	}
	if _, err := s.Movie(id); err != nil {
		t.Fatalf("不得误删影片: %v", err)
	}
	data, err := s.Data(id)
	if err != nil || !data.IsFavorite || data.PositionTicks != 123 || data.PlayCount != 2 {
		t.Fatalf("用户记录必须保留: %+v, %v", data, err)
	}
}

func TestScanChangedProtocolRemovesOldIndex(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "single"}[single], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "A.strm")
			if err := os.WriteFile(source, []byte("https://media.test/a.mp4"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "A.nfo"), []byte("<movie><title>A</title></movie>"), 0o644); err != nil {
				t.Fatal(err)
			}
			s := newStore(t, root)
			lib, err := s.AddLibrary("L", root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Scan(s, lib); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("ed2k://|file|a.mp4|1|abc|/"), 0o644); err != nil {
				t.Fatal(err)
			}
			var result Result
			if single {
				result, err = RescanOne(s, lib, source)
			} else {
				result, err = Scan(s, lib)
			}
			if err != nil || result.Skipped != 1 {
				t.Fatalf("重扫结果: %+v, %v", result, err)
			}
			if _, err := s.MovieIDByPath(source); err == nil {
				t.Fatal("非HTTP源不能保留旧索引")
			}
		})
	}
}
