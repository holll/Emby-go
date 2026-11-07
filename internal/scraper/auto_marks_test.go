package scraper

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"emby-go/internal/metatube"
	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
	"emby-go/internal/translate"
)

// Index real temporary media so tests exercise persisted ownership, not a Movie-only claim.
func autoMarksStore(t *testing.T, source string) (*store.Store, store.Movie) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auto-marks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lib, err := db.AddLibrary("marks", filepath.Dir(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(db, lib); err != nil {
		t.Fatal(err)
	}
	id, err := db.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	return db, movie
}

func TestApplyAutoMarksPolicy(t *testing.T) {
	for _, tt := range []struct {
		name, filename, tags, lock, label string
		previous, wantTags, wantOwned     []string
		overwrite, subtitle               bool
	}{
		{name: "preserve manual add owned", tags: "<tag>手工</tag>", subtitle: true, overwrite: true, label: "厂牌A", wantTags: []string{"手工", "厂牌A", "中文字幕"}, wantOwned: []string{"中文字幕"}},
		{name: "do not adopt old same text", tags: "<tag>中文字幕</tag><tag>手工</tag>", subtitle: true, overwrite: true, label: "厂牌A", wantTags: []string{"中文字幕", "手工", "厂牌A"}},
		{name: "do not adopt upstream same text", subtitle: true, overwrite: true, label: "中文字幕", wantTags: []string{"中文字幕"}},
		{name: "remove owned only without upstream label", tags: "<tag>中文字幕</tag><tag>手工</tag>", previous: []string{"中文字幕"}, overwrite: true, wantTags: []string{"手工"}},
		{name: "remove last owned tag", tags: "<tag>中文字幕</tag>", previous: []string{"中文字幕"}, overwrite: true},
		{name: "upstream takes same text", tags: "<tag>中文字幕</tag><tag>手工</tag>", previous: []string{"中文字幕"}, subtitle: true, overwrite: true, label: "中文字幕", wantTags: []string{"手工", "中文字幕"}},
		{name: "filename wins over subtitle", filename: "ABF-018-U.strm", tags: "<tag>手工</tag>", subtitle: true, overwrite: true, wantTags: []string{"手工", "无码破解"}, wantOwned: []string{"无码破解"}},
		{name: "fill existing tags", tags: "<tag>手工</tag>", subtitle: true, label: "厂牌A", wantTags: []string{"手工"}},
		{name: "fill retains existing ownership", tags: "<tag>手工</tag><tag>中文字幕</tag>", previous: []string{"中文字幕"}, wantTags: []string{"手工", "中文字幕"}, wantOwned: []string{"中文字幕"}},
		{name: "tags lock prevents adoption", tags: "<tag>手工</tag>", lock: "<lockedfields>Tags</lockedfields>", subtitle: true, overwrite: true, wantTags: []string{"手工"}},
		{name: "global lock retains ownership", tags: "<tag>手工</tag><tag>中文字幕</tag>", lock: "<lockdata>true</lockdata>", previous: []string{"中文字幕"}, overwrite: true, wantTags: []string{"手工", "中文字幕"}, wantOwned: []string{"中文字幕"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			filename := tt.filename
			if filename == "" {
				filename = "ABF-018.strm"
			}
			source := filepath.Join(dir, filename)
			if err := os.WriteFile(source, []byte("https://example.invalid/video.mp4"), 0644); err != nil {
				t.Fatal(err)
			}
			path := strings.TrimSuffix(source, ".strm") + ".nfo"
			if err := os.WriteFile(path, []byte("<movie><title>Old</title><num>ABF-018</num>"+tt.tags+tt.lock+"</movie>"), 0644); err != nil {
				t.Fatal(err)
			}
			if tt.subtitle {
				if err := os.WriteFile(strings.TrimSuffix(source, ".strm")+".zh.srt", []byte("1\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			db, movie := autoMarksStore(t, source)
			if len(tt.previous) > 0 {
				if err := db.SetAutoMarks(movie.ID, tt.previous); err != nil {
					t.Fatal(err)
				}
				movie, _ = db.Movie(movie.ID)
			}
			cfg := DefaultConfig()
			cfg.DownloadImages = false
			cfg.Translate = translate.Config{}
			s := New(cfg, db)
			result, err := s.Apply(context.Background(), movie, metatube.MovieInfo{Provider: "p", ID: "1", Number: "ABF-018", Title: "Updated", Label: tt.label}, ApplyOptions{Overwrite: tt.overwrite, Refresh: true})
			if err != nil || result.AutoMarksError != "" || result.RefreshError != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			meta, err := nfo.Read(path)
			if err != nil || !slices.Equal(meta.Tags, tt.wantTags) {
				t.Fatalf("tags=%q want=%q err=%v", meta.Tags, tt.wantTags, err)
			}
			indexed, err := db.Movie(movie.ID)
			if err != nil || !slices.Equal(indexed.AutoMarks, tt.wantOwned) || !slices.Equal(indexed.Tags, tt.wantTags) {
				t.Fatalf("movie=%+v wantOwned=%q err=%v", indexed, tt.wantOwned, err)
			}
			if tt.lock == "<lockdata>true</lockdata>" && meta.Title != "Old" {
				t.Fatalf("global lock lost: %+v", meta)
			}
		})
	}
}

func TestApplyAutoMarksUnchangedDoesNotPublishVersion(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "owned"}[marked], func(t *testing.T) {
			dir := t.TempDir()
			name := "ABF-018.strm"
			if marked {
				name = "ABF-018-C.strm"
			}
			source := filepath.Join(dir, name)
			if err := os.WriteFile(source, []byte("https://example.invalid/movie.mp4"), 0644); err != nil {
				t.Fatal(err)
			}
			db, movie := autoMarksStore(t, source)
			if marked {
				if err := os.WriteFile(strings.TrimSuffix(source, ".strm")+".nfo", []byte("<movie><tag>中文字幕</tag></movie>"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := db.SetAutoMarks(movie.ID, []string{"中文字幕"}); err != nil {
					t.Fatal(err)
				}
				var err error
				movie, err = db.Movie(movie.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := db.Version("g:version")
			cfg := DefaultConfig()
			cfg.DownloadImages = false
			cfg.Translate = translate.Config{}
			result, err := New(cfg, db).Apply(context.Background(), movie, metatube.MovieInfo{Title: "Saved"}, ApplyOptions{Overwrite: true})
			if err != nil || result.AutoMarksError != "" || db.Version("g:version") != before {
				t.Fatalf("result=%+v err=%v version=%v/%v", result, err, before, db.Version("g:version"))
			}
		})
	}
}

func TestApplyAutoMarksBatchDiagnostic(t *testing.T) {
	backend := fakeBackend(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(path, []byte("<movie><tag>手工</tag></movie>"), 0644); err != nil {
		t.Fatal(err)
	}
	movie := store.Movie{SourcePath: filepath.Join(dir, "ABF-018-C.strm"), NFOPath: path, Number: "ABF-018"}
	cfg := DefaultConfig()
	cfg.BaseURL, cfg.Token = backend.URL, "token"
	cfg.DownloadImages = false
	cfg.Translate = translate.Config{}
	var item ItemResult
	progress, err := New(cfg, nil).RunMovies(context.Background(), []store.Movie{movie}, RunOptions{Overwrite: true, OnItem: func(result ItemResult) { item = result }})
	if err != nil || progress.Done != 1 || progress.Success != 1 || progress.Failed != 0 || progress.Skipped != 0 || item.Status != StatusSuccess || item.Reason != autoMarksReviewMessage || item.Fatal {
		t.Fatalf("progress=%+v item=%+v err=%v", progress, item, err)
	}
	meta, err := nfo.Read(path)
	if err != nil || !slices.Contains(meta.Tags, "中文字幕") || !slices.Contains(meta.Tags, "手工") {
		t.Fatalf("NFO=%+v err=%v", meta, err)
	}
}

func TestApplyAutoMarksNFOFailureKeepsOwnership(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "ABF-018.strm")
	if err := os.WriteFile(source, []byte("https://example.invalid/movie.mp4"), 0644); err != nil {
		t.Fatal(err)
	}
	db, movie := autoMarksStore(t, source)
	if err := db.SetAutoMarks(movie.ID, []string{"中文字幕"}); err != nil {
		t.Fatal(err)
	}
	movie, _ = db.Movie(movie.ID)
	version := db.Version("g:version")
	movie.NFOPath = dir // A directory cannot be read or atomically replaced as an NFO.
	cfg := DefaultConfig()
	cfg.DownloadImages = false
	cfg.Translate = translate.Config{}
	result, err := New(cfg, db).Apply(context.Background(), movie, metatube.MovieInfo{Title: "Updated"}, ApplyOptions{Overwrite: true})
	if err == nil || result.AutoMarksError != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	after, err := db.Movie(movie.ID)
	if err != nil || !slices.Equal(after.AutoMarks, []string{"中文字幕"}) || db.Version("g:version") != version {
		t.Fatalf("failed NFO changed ownership: %+v %v", after, err)
	}
}

func TestApplyAutoMarksStateless(t *testing.T) {
	for _, tt := range []struct {
		name, filename, tags string
		previous             []string
		wantWarning          bool
	}{
		{name: "no ownership change", filename: "ABF-018.strm"},
		{name: "new generated tag", filename: "ABF-018-C.strm", wantWarning: true},
		{name: "unowned same text remains unowned", filename: "ABF-018-C.strm", tags: "<tag>中文字幕</tag>"},
		{name: "removed generated tag", filename: "ABF-018.strm", tags: "<tag>中文字幕</tag>", previous: []string{"中文字幕"}, wantWarning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "movie.nfo")
			if err := os.WriteFile(path, []byte("<movie>"+tt.tags+"</movie>"), 0644); err != nil {
				t.Fatal(err)
			}
			movie := store.Movie{SourcePath: filepath.Join(dir, tt.filename), NFOPath: path, AutoMarks: tt.previous}
			cfg := DefaultConfig()
			cfg.DownloadImages = false
			cfg.Translate = translate.Config{}
			s := New(cfg, nil)
			result, err := s.Apply(context.Background(), movie, metatube.MovieInfo{Title: "Saved"}, ApplyOptions{Overwrite: true})
			if err != nil || (result.AutoMarksError != "") != tt.wantWarning {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			meta, err := nfo.Read(path)
			if err != nil || !strings.Contains(meta.Title, "Saved") {
				t.Fatalf("meta=%+v err=%v", meta, err)
			}
		})
	}
}
