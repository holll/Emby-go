package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"emby-go/internal/nfo"
	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

func translationTestScraper(t *testing.T, handler http.HandlerFunc, st *store.Store) *Scraper {
	t.Helper()
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)
	cfg := DefaultConfig()
	cfg.Translate.APIURL = service.URL
	cfg.Translate.APIKey = "test-key"
	// A MetaTube access would fail the operation: there is no listener here.
	cfg.BaseURL = "http://127.0.0.1:1"
	cfg.Token = "not-used"
	cfg.TranslateTitle = false
	cfg.TranslatePlot = false
	return New(cfg, st)
}

func TestTranslateExistingSourcesOnlyAndFieldLocks(t *testing.T) {
	for _, tc := range []struct {
		name, xml         string
		opts              TranslationOptions
		input             []string
		title, sort, plot string
	}{
		{"both", `<originaltitle>原題</originaltitle><title>old translated</title><plot>current plot</plot>`, TranslationOptions{Title: true, Plot: true}, []string{"原題", "current plot"}, "ABC-123 译文0", "ABC-123 译文0", "译文1"},
		{"title-locked", `<originaltitle>原題</originaltitle><title>manual</title><plot>current plot</plot><lockedfields>Name</lockedfields>`, TranslationOptions{Title: true, Plot: true}, []string{"current plot"}, "manual", "old sort", "译文0"},
		{"sort-locked", `<originaltitle>原題</originaltitle><title>old</title><plot>current plot</plot><lockedfields>SortName</lockedfields>`, TranslationOptions{Title: true}, []string{"原題"}, "ABC-123 译文0", "old sort", "current plot"},
		{"missing-original", `<title>never translate this</title><plot>current plot</plot>`, TranslationOptions{Title: true}, nil, "never translate this", "old sort", "current plot"},
		{"plot-locked", `<title>old</title><originaltitle>原題</originaltitle><plot>manual plot</plot><lockedfields>Overview</lockedfields>`, TranslationOptions{Plot: true}, nil, "old", "old sort", "manual plot"},
		{"unselected", `<title>old</title><originaltitle>原題</originaltitle><plot>current plot</plot>`, TranslationOptions{}, nil, "old", "old sort", "current plot"},
		{"empty-plot", `<title>old</title><originaltitle>原題</originaltitle><plot></plot>`, TranslationOptions{Plot: true}, nil, "old", "old sort", ""},
		{"global-lock", `<title>old</title><originaltitle>原題</originaltitle><plot>manual plot</plot><lockdata>true</lockdata>`, TranslationOptions{Title: true, Plot: true}, nil, "old", "old sort", "manual plot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			var calls atomic.Int32
			done := make(chan struct{})
			s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				calls.Add(1)
				var req struct {
					Text []string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				got = req.Text
				texts := make([]map[string]string, len(req.Text))
				for i := range texts {
					texts[i] = map[string]string{"text": "译文" + string(rune('0'+i))}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"translations": texts})
			}, nil)
			path := filepath.Join(t.TempDir(), "movie.nfo")
			before := `<movie><num>abc123</num><sorttitle>old sort</sorttitle>` + tc.xml + `<extension foo="keep"><plot>nested</plot></extension></movie>`
			if err := os.WriteFile(path, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := s.TranslateExisting(context.Background(), store.Movie{ID: 4, NFOPath: path, Number: "WRONG-999"}, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if tc.input != nil {
				<-done
			}
			if !reflect.DeepEqual(got, tc.input) {
				t.Fatalf("source=%v want=%v", got, tc.input)
			}
			if (tc.input == nil) != result.Skipped {
				t.Fatalf("skip=%+v", result)
			}
			if tc.input == nil && calls.Load() != 0 {
				t.Fatal("unnecessary translation request")
			}
			raw, _ := os.ReadFile(path)
			for tag, want := range map[string]string{"title": tc.title, "sorttitle": tc.sort, "plot": tc.plot} {
				if v := nfo.ScrapedTagValue(string(raw), tag); v != want {
					t.Fatalf("%s=%q want=%q", tag, v, want)
				}
			}
			if !strings.Contains(string(raw), `<extension foo="keep"><plot>nested</plot></extension>`) {
				t.Fatal("extension lost")
			}
			if nfo.ScrapedTagValue(string(raw), "originaltitle") != nfo.ScrapedTagValue(before, "originaltitle") {
				t.Fatal("originaltitle changed")
			}
		})
	}
}

func TestTranslateExistingFailureCancellationAndConcurrentEdit(t *testing.T) {
	for _, mode := range []string{"service", "cancel", "conflict", "unrelated-edit"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.nfo")
			const before = `<movie><title>old</title><originaltitle>source</originaltitle><plot>summary</plot><custom>keep</custom></movie>`
			if err := os.WriteFile(path, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			edited := before
			done := make(chan struct{})
			s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				switch mode {
				case "service":
					http.Error(w, "unavailable", 503)
					return
				case "cancel":
					cancel()
				case "conflict":
					// Use the shared writer: proves the network call holds no path lock.
					if err := nfo.UpdateScraped(path, nfo.ScrapeFields{Title: "external", Overwrite: true}); err != nil {
						t.Error(err)
					}
					b, _ := os.ReadFile(path)
					edited = string(b)
				case "unrelated-edit":
					edited = strings.Replace(before, "<custom>keep</custom>", "<custom>external</custom>", 1)
					if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
						t.Error(err)
					}
				}
				_, _ = w.Write([]byte(`{"translations":[{"text":"translated"}]}`))
			}, nil)
			result, err := s.TranslateExisting(ctx, store.Movie{ID: 1, NFOPath: path}, TranslationOptions{Title: true})
			<-done
			if mode == "unrelated-edit" {
				if err != nil || !result.Changed {
					t.Fatalf("%+v %v", result, err)
				}
				b, _ := os.ReadFile(path)
				if !strings.Contains(string(b), "<custom>external</custom>") {
					t.Fatal("lost external XML")
				}
				return
			}
			if err == nil || result.Changed {
				t.Fatalf("%+v %v", result, err)
			}
			if mode == "conflict" && !errors.Is(err, nfo.ErrTranslationConflict) {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			if string(b) != edited {
				t.Fatalf("failed translation overwrote NFO: %s", b)
			}
		})
	}
}

func TestTranslateExistingStackedRefreshAndNoScrapeChanges(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "ABC-123-CD1.strm")
	second := filepath.Join(dir, "ABC-123-CD2.strm")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("https://media.test/movie.mp4\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(dir, "ABC-123.nfo")
	if err := os.WriteFile(shared, []byte(`<movie><num>ABC-123</num><originaltitle>source</originaltitle><title>old</title><plot>plot</plot><uniqueid type="metatube">untouched</uniqueid><unknown>keep</unknown></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := db.AddLibrary("test", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(db, lib); err != nil {
		t.Fatal(err)
	}
	id, err := db.MovieIDByPath(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetScrapeResult(id, "keep prior scrape failure"); err != nil {
		t.Fatal(err)
	}
	movie, err := db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"translations":[{"text":"translated"}]}`))
	}, db)
	// Exercise SidecarPath fallback for a stacked item, without making a CD1 NFO.
	movie.NFOPath = ""
	version := db.Version("g:version")
	result, err := s.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true})
	if err != nil || !result.Changed || result.RefreshError != "" {
		t.Fatalf("%+v %v", result, err)
	}
	stale, err := db.Movie(id)
	if err != nil || stale.Title != movie.Title || db.Version("g:version") != version {
		t.Fatalf("Refresh=false touched index: %+v %v", stale, err)
	}
	// A no-op disk retry must still repair an index when Refresh is requested.
	result, err = s.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true, Refresh: true})
	if err != nil || result.Changed || result.RefreshError != "" {
		t.Fatalf("no-op refresh: %+v %v", result, err)
	}
	indexed, err := db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.Title != "ABC-123 translated" || indexed.NFOPath != shared || len(indexed.AdditionalParts) != 1 {
		t.Fatalf("indexed=%+v", indexed)
	}
	if indexed.LastScrapeAt != movie.LastScrapeAt || indexed.LastScrapeError != movie.LastScrapeError {
		t.Fatalf("translation changed scrape result: before=%+v after=%+v", movie, indexed)
	}
	if db.Version("g:version") == version {
		t.Fatal("index version not published")
	}
	if _, err := os.Stat(filepath.Join(dir, "ABC-123-CD1.nfo")); !os.IsNotExist(err) {
		t.Fatalf("unexpected CD1 NFO: %v", err)
	}
	b, _ := os.ReadFile(shared)
	if !strings.Contains(string(b), `<uniqueid type="metatube">untouched</uniqueid>`) {
		t.Fatal("scrape provider mutated")
	}
}

func TestTranslateExistingRegisteredPathAndDisabledService(t *testing.T) {
	dir := t.TempDir()
	registered := filepath.Join(dir, "legacy-CD1.nfo")
	const before = `<movie><originaltitle>source</originaltitle><title>old</title></movie>`
	if err := os.WriteFile(registered, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	movie := store.Movie{SourcePath: filepath.Join(dir, "legacy-CD1.strm"), NFOPath: registered, AdditionalParts: []string{filepath.Join(dir, "legacy-CD2.strm")}}
	disabled := New(DefaultConfig(), nil)
	if result, err := disabled.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true}); err == nil || result.Changed {
		t.Fatalf("disabled service: %+v %v", result, err)
	}
	b, _ := os.ReadFile(registered)
	if string(b) != before {
		t.Fatal("disabled translation changed NFO")
	}
	s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"translations":[{"text":"translated"}]}`))
	}, nil)
	if result, err := s.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true}); err != nil || !result.Changed {
		t.Fatalf("registered path: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "legacy.nfo")); !os.IsNotExist(err) {
		t.Fatalf("migrated registered NFO: %v", err)
	}
	b, _ = os.ReadFile(registered)
	if !strings.Contains(string(b), "<title>translated</title>") {
		t.Fatal("registered NFO not updated")
	}
}

func translationRealIndexFixture(t *testing.T, legacy bool) (*store.Store, store.Movie, string, string) {
	t.Helper()
	dir := t.TempDir()
	first := filepath.Join(dir, "LEG-123-CD1.strm")
	second := filepath.Join(dir, "LEG-123-CD2.strm")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("https://media.test/movie.mp4\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(dir, "LEG-123.nfo")
	if err := os.WriteFile(shared, []byte(`<movie><num>LEG-123</num><originaltitle>shared source</originaltitle><title>shared old</title><shared-extension>keep shared</shared-extension></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	if legacy {
		if err := os.WriteFile(filepath.Join(dir, "LEG-123-CD1.nfo"), []byte(`<movie><num>LEG-123</num><originaltitle>legacy source</originaltitle><title>legacy old</title><legacy-extension>keep legacy</legacy-extension></movie>`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "translation.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lib, err := db.AddLibrary("translation", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(db, lib); err != nil {
		t.Fatal(err)
	}
	id, err := db.MovieIDByPath(first)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := db.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(movie.AdditionalParts) != 1 || movie.AdditionalParts[0] != second {
		t.Fatalf("bad stacked fixture: %+v", movie)
	}
	return db, movie, dbPath, shared
}

func TestTranslateExistingRealRefreshFailureRepair(t *testing.T) {
	db, movie, dbPath, _ := translationRealIndexFixture(t, false)
	beforeVersion := db.Version("g:version")
	s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"translations":[{"text":"translated"}]}`))
	}, db)
	// A real, nonnil Store fails inside rescan's Library lookup, after the
	// translation has committed. Closing only this temporary fixture is safe.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := s.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true, Refresh: true})
	if err != nil || !result.Changed || result.RefreshError == "" {
		t.Fatalf("persisted translation/failed refresh: %+v %v", result, err)
	}
	meta, err := nfo.Read(movie.NFOPath)
	if err != nil || meta.Title != "LEG-123 translated" {
		t.Fatalf("translation did not persist: %+v %v", meta, err)
	}
	recovered, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	s.store = recovered
	stale, err := recovered.Movie(movie.ID)
	if err != nil || stale.Title != movie.Title || recovered.Version("g:version") != beforeVersion {
		t.Fatalf("failed refresh changed index: %+v %v", stale, err)
	}
	result, err = s.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true, Refresh: true})
	if err != nil || result.Changed || result.RefreshError != "" {
		t.Fatalf("no-change recovery refresh: %+v %v", result, err)
	}
	indexed, err := recovered.Movie(movie.ID)
	if err != nil || indexed.Title != "LEG-123 translated" || indexed.NFOPath != movie.NFOPath || !reflect.DeepEqual(indexed.AdditionalParts, movie.AdditionalParts) {
		t.Fatalf("recovery index: %+v %v", indexed, err)
	}
	if recovered.Version("g:version") == beforeVersion {
		t.Fatal("repaired index did not publish version")
	}
}

func TestTranslateExistingLegacyAndSharedRefresh(t *testing.T) {
	db, movie, _, shared := translationRealIndexFixture(t, true)
	registered := filepath.Join(filepath.Dir(shared), "LEG-123-CD1.nfo")
	if movie.NFOPath != registered || movie.Title != "legacy old" {
		t.Fatalf("fixture did not register legacy NFO: %+v", movie)
	}
	sharedBefore, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		var req struct {
			Text []string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(req.Text, []string{"legacy source"}) {
			t.Errorf("translated wrong sidecar: %v", req.Text)
		}
		_, _ = w.Write([]byte(`{"translations":[{"text":"translated legacy"}]}`))
	}, db)
	result, err := s.TranslateExisting(context.Background(), movie, TranslationOptions{Title: true, Refresh: true})
	<-done
	if err != nil || !result.Changed || result.RefreshError != "" {
		t.Fatalf("legacy refresh: %+v %v", result, err)
	}
	indexed, err := db.Movie(movie.ID)
	if err != nil || indexed.Title != "LEG-123 translated legacy" || indexed.NFOPath != registered || indexed.SourcePath != movie.SourcePath || !reflect.DeepEqual(indexed.AdditionalParts, movie.AdditionalParts) {
		t.Fatalf("legacy index: %+v %v", indexed, err)
	}
	sharedAfter, err := os.ReadFile(shared)
	if err != nil || string(sharedAfter) != string(sharedBefore) {
		t.Fatalf("shared NFO changed: %s %v", sharedAfter, err)
	}
	registeredAfter, err := os.ReadFile(registered)
	if err != nil || !strings.Contains(string(registeredAfter), "<title>LEG-123 translated legacy</title>") || !strings.Contains(string(registeredAfter), "<legacy-extension>keep legacy</legacy-extension>") {
		t.Fatalf("registered NFO: %s %v", registeredAfter, err)
	}
}

func TestTranslateExistingMissingAndRefreshFailure(t *testing.T) {
	s := translationTestScraper(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"translations":[{"text":"translated"}]}`))
	}, nil)
	path := filepath.Join(t.TempDir(), "missing.nfo")
	result, err := s.TranslateExisting(context.Background(), store.Movie{NFOPath: path}, TranslationOptions{Title: true})
	if err != nil || !result.Skipped {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created missing sidecar: %v", err)
	}
	if err := os.WriteFile(path, []byte(`<movie><originaltitle>source</originaltitle></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = s.TranslateExisting(context.Background(), store.Movie{NFOPath: path}, TranslationOptions{Title: true, Refresh: true})
	if err != nil || !result.Changed || result.RefreshError == "" {
		t.Fatalf("persisted refresh failure lost: %+v %v", result, err)
	}
}
