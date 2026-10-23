package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranslationCASAndPreservation(t *testing.T) {
	const initial = `<?xml version="1.0"?><movie><num>ABC-123</num><title>old</title><originaltitle>original</originaltitle><sorttitle>old sort</sorttitle><plot>source plot</plot><!--keep--><extra attr="1"><title>nested</title></extra></movie>`
	for _, tag := range []string{"title", "originaltitle", "sorttitle", "num", "plot", "lockedfields", "lockdata"} {
		t.Run(tag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.nfo")
			if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			snap, err := ReadTranslationSnapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			concurrent := upsertScalar(initial, tag, "external edit", "  ")
			if err := os.WriteFile(path, []byte(concurrent), 0600); err != nil {
				t.Fatal(err)
			}
			if changed, err := CommitTranslation(context.Background(), path, snap, "译名", "译简介"); changed || !errors.Is(err, ErrTranslationConflict) {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			raw, _ := os.ReadFile(path)
			if string(raw) != concurrent {
				t.Fatal("conflicting edit overwritten")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "movie.nfo")
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadTranslationSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	external := strings.Replace(initial, "<!--keep-->", "<!--external-->", 1)
	if err := os.WriteFile(path, []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := CommitTranslation(context.Background(), path, snap, "译名", "译简介")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(path)
	for _, keep := range []string{`<?xml version="1.0"?>`, `<!--external-->`, `<extra attr="1"><title>nested</title></extra>`, `<originaltitle>original</originaltitle>`, `<title>译名</title>`, `<sorttitle>译名</sorttitle>`} {
		if !strings.Contains(string(raw), keep) {
			t.Fatalf("lost %s in %s", keep, raw)
		}
	}
}

func TestTranslationCancellationMissingAndSortLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.nfo")
	if _, err := ReadTranslationSnapshot(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	const raw = `<movie><originaltitle>source</originaltitle><title>old</title><sorttitle>manual</sorttitle><lockedfields>SortName</lockedfields></movie>`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadTranslationSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if changed, err := CommitTranslation(ctx, path, snap, "translated", ""); changed || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %v", changed, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != raw {
		t.Fatal("canceled write happened")
	}
	if _, err := CommitTranslation(context.Background(), path, snap, "translated", ""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "<sorttitle>manual</sorttitle>") {
		t.Fatal("sort lock ignored")
	}
}
