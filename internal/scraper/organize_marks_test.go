package scraper

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDetectSubtitleMarkEvidence(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Movie.zh-CN.srt", "Movie.en.ZH-hant.ASS", "Movie.srt", "Movie.en.srt", "Movie2.zh.srt", "Movie.zh.txt", "Movie-CD2.zh.srt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "Movie.chs.ssa"), 0700); err != nil {
		t.Fatal(err)
	}
	evidence, err := DetectSubtitleMark(filepath.Join(dir, "Movie.strm"))
	if err != nil || evidence.Code != "C" || evidence.Tag != "中文字幕" || evidence.Source != "subtitle" || !slices.Equal(evidence.Matches, []string{"Movie.en.ZH-hant.ASS", "Movie.zh-CN.srt"}) {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	for _, code := range []string{"C", "U", "UC"} {
		evidence, err := DetectSubtitleMark(filepath.Join(dir, "Movie-"+code+".strm"))
		mark, _ := markByCode(code)
		if err != nil || evidence.Code != code || evidence.Tag != mark.Tag || evidence.Source != "filename" || len(evidence.Matches) != 0 {
			t.Fatalf("evidence=%+v err=%v", evidence, err)
		}
	}
	for _, name := range []string{"Movie2x.strm", "Movie-CD1.strm", "MovieC.strm"} {
		evidence, err := DetectSubtitleMark(filepath.Join(dir, name))
		if err != nil || evidence.Source != "none" || evidence.Code != "" || evidence.Tag != "" || evidence.Matches == nil || len(evidence.Matches) != 0 {
			t.Fatalf("evidence=%+v err=%v", evidence, err)
		}
	}
}

func TestDetectSubtitleMarkDirectoryFailureAndLegacyCompatibility(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	source := filepath.Join(dir, "Movie.strm")
	if _, err := DetectSubtitleMark(source); err == nil {
		t.Fatal("unreadable directory treated as absent subtitles")
	}
	if _, ok := versionMarkFor(source); ok {
		t.Fatal("legacy behavior changed")
	}
	for _, code := range []string{"C", "U", "UC"} {
		evidence, err := DetectSubtitleMark(filepath.Join(dir, "Movie-"+code+".strm"))
		if err != nil || evidence.Code != code || evidence.Source != "filename" {
			t.Fatalf("explicit suffix accessed directory: %+v %v", evidence, err)
		}
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectSubtitleMark(filepath.Join(file, "Movie.strm")); err == nil {
		t.Fatal("non-directory accepted")
	}
}

func TestDetectSubtitleMarkChineseTokensMatchLegacy(t *testing.T) {
	for token := range chineseSubtitleTokens {
		t.Run(token, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "movie."+token+".srt"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(dir, "movie.strm")
			evidence, err := DetectSubtitleMark(source)
			legacy, ok := versionMarkFor(source)
			if err != nil || !ok || evidence.Code != legacy.Code || evidence.Tag != legacy.Tag || len(evidence.Matches) != 1 {
				t.Fatalf("new=%+v legacy=%+v ok=%v err=%v", evidence, legacy, ok, err)
			}
		})
	}
}
