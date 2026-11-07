package nfo

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOrganizeAutoMarksPolicy(t *testing.T) {
	for _, tt := range []struct {
		name, body                      string
		previous, current, after, owned []string
		overwrite, protected            bool
	}{
		{name: "replace owned", body: "<tag>custom</tag><tag>old</tag>", previous: []string{"old"}, current: []string{"new"}, after: []string{"custom", "new"}, owned: []string{"new"}, overwrite: true},
		{name: "unowned same name", body: "<tag>mark</tag>", current: []string{"mark"}, after: []string{"mark"}, overwrite: true},
		{name: "empty withdrawal", body: "<tag>old</tag>", previous: []string{"old"}, overwrite: true},
		{name: "fill empty", current: []string{"mark"}, after: []string{"mark"}, owned: []string{"mark"}},
		{name: "fill protects", body: "<tag>mark</tag>", previous: []string{"mark", "gone"}, current: []string{"new"}, after: []string{"mark"}, owned: []string{"mark"}, protected: true},
		{name: "empty tag protects", body: "<tag/>", after: []string{""}, protected: true},
		{name: "tag lock", body: "<lockedfields>Tags</lockedfields><tag>mark</tag>", previous: []string{"mark", "gone"}, current: []string{"new"}, after: []string{"mark"}, owned: []string{"mark"}, overwrite: true, protected: true},
		{name: "global lock", body: "<lockdata>true</lockdata>", current: []string{"new"}, overwrite: true, protected: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := "<movie>" + tt.body + "</movie>"
			path := writeTempNFO(t, original)
			plan, err := PreviewAutoMarks(path, tt.previous, tt.current, tt.overwrite)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(plan.After, tt.after) || !slices.Equal(plan.Owned, tt.owned) || plan.Protected != tt.protected {
				t.Fatalf("plan=%+v", plan)
			}
			if plan.Digest != fmt.Sprintf("%x", sha256.Sum256([]byte(original))) || readTempNFO(t, path) != original {
				t.Fatal("preview changed input or wrong digest")
			}
			committed, err := CommitAutoMarks(path, tt.previous, tt.current, tt.overwrite, plan.Digest)
			if err != nil || !reflect.DeepEqual(committed, plan) {
				t.Fatalf("commit=%+v err=%v", committed, err)
			}
			if !plan.Changed && readTempNFO(t, path) != original {
				t.Fatal("no-op rewrote original")
			}
			// Existing scraping uses exactly the same policy, including blank tag protection.
			scrape := writeTempNFO(t, original)
			owned, err := UpdateScrapedWithAutoMarks(scrape, ScrapeFields{Overwrite: tt.overwrite}, tt.previous, tt.current)
			if err != nil || !slices.Equal(owned, plan.Owned) {
				t.Fatalf("scrape ownership=%q err=%v", owned, err)
			}
			// Compare parsed direct tags, including the protected empty tag.
			other, err := PreviewAutoMarks(scrape, nil, nil, false)
			if err != nil || !slices.Equal(other.Before, plan.After) {
				t.Fatalf("scrape tags=%q err=%v", other.Before, err)
			}
		})
	}
}

func TestOrganizeAutoMarksRejectsMissingBrokenAndConflict(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.nfo")
	for _, path := range []string{missing, writeTempNFO(t, "<movie><tag>broken"), writeTempNFO(t, "<other></other>")} {
		if _, err := PreviewAutoMarks(path, nil, []string{"new"}, true); err == nil {
			t.Fatalf("preview accepted %s", path)
		}
		if _, err := CommitAutoMarks(path, nil, []string{"new"}, true, ""); err == nil {
			t.Fatalf("commit accepted %s", path)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created missing NFO")
	}
	path := writeTempNFO(t, "<movie><title>before</title></movie>")
	plan, err := PreviewAutoMarks(path, nil, []string{"new"}, true)
	if err != nil {
		t.Fatal(err)
	}
	changed := "<movie><title>external</title></movie>"
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{plan.Digest, ""} {
		if _, err := CommitAutoMarks(path, nil, []string{"new"}, true, digest); !errors.Is(err, ErrAutoMarksConflict) {
			t.Fatalf("err=%v", err)
		}
	}
	if readTempNFO(t, path) != changed {
		t.Fatal("conflict wrote file")
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("conflict left temporary file")
	}
}

func TestOrganizeAutoMarksPreservesBytesAndNoOp(t *testing.T) {
	original := "\xef\xbb\xbf<?xml version=\"1.0\"?>\r\n<movie custom='value'>\r\n\t<!--keep-->\r\n\t<title a='b'>A &amp; B</title>\r\n\t<actor><name>A</name><tag>nested</tag></actor>\r\n\t<x:tag xmlns:x='urn:x'>extension</x:tag>\r\n\t<tag>old</tag>\r\n</movie>\r\n"
	path := writeTempNFO(t, original)
	plan, err := PreviewAutoMarks(path, []string{"old"}, []string{"new"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAutoMarks(path, []string{"old"}, []string{"new"}, true, plan.Digest); err != nil {
		t.Fatal(err)
	}
	actual := readTempNFO(t, path)
	// Remove only the old/new direct tag lines to compare all unrelated bytes.
	if strings.Replace(original, "\t<tag>old</tag>\r\n", "", 1) != strings.Replace(actual, "\t<tag>new</tag>\n", "", 1) {
		t.Fatalf("unrelated bytes changed:\n%q", actual)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1000000000, 0)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	plan, err = PreviewAutoMarks(path, []string{"new"}, []string{"new"}, true)
	if err != nil || plan.Changed {
		t.Fatalf("no-op=%+v %v", plan, err)
	}
	if _, err := CommitAutoMarks(path, []string{"new"}, []string{"new"}, true, plan.Digest); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(fixed) || !os.SameFile(info, after) {
		t.Fatal("no-op rewrote file")
	}
}

func TestOrganizeAutoMarksSharesExistingPathLock(t *testing.T) {
	path := writeTempNFO(t, "<movie><actor><name>actor</name></actor></movie>")
	unlock := lockNFOPath(path)
	results := make(chan error, 2)
	go func() { _, err := PreviewAutoMarks(path, nil, nil, true); results <- err }()
	go func() { _, err := CommitAutoMarks(path, nil, nil, true, ""); results <- err }()
	select {
	case err := <-results:
		unlock()
		t.Fatalf("operation escaped existing lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	for range 2 {
		err := <-results
		if err != nil && !errors.Is(err, ErrAutoMarksConflict) {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			plan, err := PreviewAutoMarks(path, nil, []string{"mark"}, true)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = CommitAutoMarks(path, nil, []string{"mark"}, true, plan.Digest)
			if err != nil && !errors.Is(err, ErrAutoMarksConflict) {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := SetActorThumb(path, "actor", "thumb"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	meta, err := Read(path)
	if err != nil || len(meta.Actors) != 1 || meta.Actors[0].Thumb != "thumb" || !slices.Contains(meta.Tags, "mark") {
		t.Fatalf("lost updates: %+v %v", meta, err)
	}
}
