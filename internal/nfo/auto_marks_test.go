package nfo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestUpdateScrapedWithAutoMarksMerge(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		old, upstream, previous []string
		current, tags, owned    []string
	}{
		{name: "preserve unowned history", old: []string{"custom", "C", "UC", "中文字幕"}, current: []string{"中文字幕"}, tags: []string{"custom", "C", "UC", "中文字幕"}},
		{name: "replace only owned", old: []string{"custom", "old provider", "中文字幕"}, previous: []string{"中文字幕"}, upstream: []string{"new provider"}, current: []string{"无码"}, tags: []string{"custom", "old provider", "new provider", "无码"}, owned: []string{"无码"}},
		{name: "withdraw all", old: []string{"中文字幕"}, previous: []string{"中文字幕"}},
		{name: "withdraw preserves custom", old: []string{"custom", "中文字幕"}, previous: []string{"中文字幕"}, tags: []string{"custom"}},
		{name: "existing ownership authoritative", old: []string{"中文字幕"}, previous: []string{"中文字幕"}, current: []string{"中文字幕"}, tags: []string{"中文字幕"}, owned: []string{"中文字幕"}},
		{name: "upstream collision", upstream: []string{"中文字幕"}, current: []string{"中文字幕"}, tags: []string{"中文字幕"}},
		{name: "previous now upstream", old: []string{"中文字幕"}, previous: []string{"中文字幕"}, upstream: []string{"中文字幕"}, current: []string{"中文字幕"}, tags: []string{"中文字幕"}},
		{name: "missing previous not retained", previous: []string{"missing"}, current: []string{"new"}, tags: []string{"new"}, owned: []string{"new"}},
		{name: "empty", old: []string{"custom"}, tags: []string{"custom"}},
		{name: "trim and deduplicate inputs", upstream: []string{" provider ", "provider"}, current: []string{" mark ", "mark", " "}, tags: []string{"provider", "mark"}, owned: []string{"mark"}},
		{name: "escaped values", old: []string{"custom & <value>"}, current: []string{"mark & <new>"}, tags: []string{"custom & <value>", "mark & <new>"}, owned: []string{"mark & <new>"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := "<movie>\n<!--keep-->\n<custom><tag>nested</tag></custom>\n<ext:tag xmlns:ext=\"urn:other\">extension</ext:tag>\n" + strings.Join(renderList("tag", tt.old, ""), "") + "</movie>"
			path := writeTempNFO(t, body)
			owned, err := UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: true, Tags: tt.upstream, Title: "updated"}, tt.previous, tt.current)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(owned, tt.owned) {
				t.Fatalf("owned = %q, want %q", owned, tt.owned)
			}
			text := readTempNFO(t, path)
			var tags []string
			for _, element := range elementsNamed(text, "tag") {
				tags = append(tags, ScrapedTagValue("<movie>"+text[element.start:element.end]+"</movie>", "tag"))
			}
			if !slices.Equal(tags, tt.tags) {
				t.Fatalf("tags = %q, want %q\n%s", tags, tt.tags, text)
			}
			for _, keep := range []string{"<!--keep-->", "<custom><tag>nested</tag></custom>", "<ext:tag xmlns:ext=\"urn:other\">extension</ext:tag>", "<title>updated</title>"} {
				if !strings.Contains(text, keep) {
					t.Errorf("lost %q: %s", keep, text)
				}
			}
		})
	}
}

func TestUpdateScrapedWithAutoMarksProtected(t *testing.T) {
	for _, tt := range []struct {
		name, tags, lock string
		overwrite        bool
		previous, owned  []string
	}{
		{name: "fill existing unowned", tags: "<tag>mark</tag>"},
		{name: "fill retains previous intersection", tags: "<tag>mark</tag><tag>custom</tag>", previous: []string{"mark", "gone", "mark"}, owned: []string{"mark"}},
		{name: "fill empty block", tags: "<tag/>"},
		{name: "tags lock", tags: "<tag>mark</tag>", lock: "<lockedfields>Tags</lockedfields>", overwrite: true, previous: []string{"mark", "gone"}, owned: []string{"mark"}},
		{name: "tags lock empty", lock: "<lockedfields>Tags</lockedfields>", overwrite: true},
		{name: "global lock", tags: "<tag>mark</tag>", lock: "<lockdata>true</lockdata>", overwrite: true, previous: []string{"mark"}, owned: []string{"mark"}},
		{name: "global lock empty", lock: "<lockdata>true</lockdata>", overwrite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := "<movie>" + tt.tags + tt.lock + "</movie>"
			path := writeTempNFO(t, body)
			owned, err := UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: tt.overwrite, Tags: []string{"upstream"}}, tt.previous, []string{"mark", "new"})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(owned, tt.owned) || readTempNFO(t, path) != body {
				t.Fatalf("protected document changed: owned=%q want=%q text=%s", owned, tt.owned, readTempNFO(t, path))
			}
		})
	}
}

func TestUpdateScrapedWithAutoMarksCreateAndFill(t *testing.T) {
	for _, create := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "movie.nfo")
		if !create {
			if err := os.WriteFile(path, []byte("<movie></movie>"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		owned, err := UpdateScrapedWithAutoMarks(path, ScrapeFields{Title: "title", Tags: []string{"upstream", "collision"}}, []string{"old"}, []string{"collision", "new"})
		if err != nil || !slices.Equal(owned, []string{"new"}) {
			t.Fatalf("create=%v owned=%q err=%v", create, owned, err)
		}
		meta, err := Read(path)
		if err != nil || meta.Title != "title" || !slices.Equal(meta.Tags, []string{"upstream", "collision", "new"}) {
			t.Fatalf("create=%v meta=%+v err=%v", create, meta, err)
		}
	}
}

func TestUpdateScrapedWithAutoMarksErrors(t *testing.T) {
	broken := writeTempNFO(t, "<movie><tag>old</tag>")
	writeFailure := writeTempNFO(t, "<movie><tag>old</tag></movie>")
	if err := os.Mkdir(writeFailure+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	createFailure := filepath.Join(t.TempDir(), "movie.nfo")
	if err := os.Mkdir(createFailure+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{broken, t.TempDir(), writeFailure, createFailure} {
		owned, err := UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: true}, []string{"old"}, []string{"new"})
		if err == nil || len(owned) != 0 {
			t.Errorf("path=%q returned owned=%q err=%v", path, owned, err)
		}
	}
	if readTempNFO(t, broken) != "<movie><tag>old</tag>" || readTempNFO(t, writeFailure) != "<movie><tag>old</tag></movie>" {
		t.Fatal("failed update changed original file")
	}
}

func TestUpdateScrapedWithAutoMarksSharesPathLock(t *testing.T) {
	path := writeTempNFO(t, "<movie><tag>custom</tag><actor><name>actor</name></actor></movie>")
	// All writers change different fields, so a lost read-modify-write is observable.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, update := range []func() error{
			func() error {
				_, err := UpdateScrapedWithAutoMarks(path, ScrapeFields{Overwrite: true}, nil, []string{"mark"})
				return err
			},
			func() error {
				return UpdateScraped(path, ScrapeFields{Title: "manual", Overwrite: true, ManualEdit: true})
			},
			func() error {
				_, err := SetActorThumb(path, "actor", "thumb")
				return err
			},
		} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := update(); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	meta, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "manual" || !slices.Equal(meta.Tags, []string{"custom", "mark"}) || len(meta.Actors) != 1 || meta.Actors[0].Thumb != "thumb" {
		t.Fatalf("lost concurrent update: %+v", meta)
	}
}
