package store

import (
	"fmt"
	"testing"
)

// TestFtsSyncAndSearchSemantics verifies migration22 search semantics:
// trigram substring for >=3 runes, LIKE fallback for short terms, and
// movie_features equality for genre/tag/studio. Triggers keep the FTS
// index in sync with movies writes after migration backfill.
func TestFtsSyncAndSearchSemantics(t *testing.T) {
	s, lib := newFeatureStore(t)

	id1 := upsertFixtureMovie(t, s, lib, Movie{
		LibraryID: lib, SourcePath: "/tmp/av/fts1.strm", Status: "success",
		Title: "Hello World", OriginalTitle: "Orig Title", Number: "ABP-123",
		Genres: []string{"Drama", "剧情"}, Tags: []string{"单体"}, Studios: []string{"Studio X"},
	})
	s.waitFeaturesReady()

	// FTS backfill from migration: existing movies are searchable.
	if got := ftsSearchCount(t, s, "Hello"); got != 1 {
		t.Fatalf("backfilled FTS missed Hello: %d", got)
	}
	if got := ftsSearchCount(t, s, "ABP"); got != 1 {
		t.Fatalf("backfilled FTS missed ABP: %d", got)
	}

	// Substring (not prefix): trigram phrase match contains "ello".
	if got := ftsSearchCount(t, s, "ello"); got != 1 {
		t.Fatalf("trigram substring ello: %d", got)
	}
	// Phrase with space is a quoted phrase in trigram index.
	if got := ftsSearchCount(t, s, "lo Wo"); got != 1 {
		t.Fatalf("trigram phrase 'lo Wo': %d", got)
	}
	// Quote inside term must be escaped to "" — search() does this;
	// direct FTS helper passes raw term, so use search() for the quote case.
	_, totalQ, err := s.search(0, lib, nil, `"llo`, "success", "", "", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if totalQ != 0 {
		t.Fatalf("quote term via search() total=%d want 0 (escaped, no match)", totalQ)
	}

	// Short terms (<3 runes) use LIKE fallback in search().
	// LIKE is case-insensitive for ASCII: 'ab' matches ABP-123.
	movies, total, err := s.search(0, lib, nil, "ab", "success", "", "", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("short LIKE fallback 'ab' total=%d want 1 (case-insensitive)", total)
	}
	_ = movies
	_, total2, err := s.search(0, lib, nil, "AB", "success", "", "", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 1 {
		t.Fatalf("short LIKE fallback 'AB' total=%d want 1", total2)
	}
	// lowercase 'ab' also matches ABP-123 via case-insensitive LIKE.
	_, totalAb, err := s.search(0, lib, nil, "ab", "success", "", "", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if totalAb != 1 {
		t.Fatalf("short LIKE fallback 'ab' total=%d want 1 (case-insensitive)", totalAb)
	}

	// Entity filter via movie_features equality (not JSON LIKE).
	movies, total, err = s.search(0, lib, nil, "", "success", "", "Drama", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(movies) != 1 || movies[0].ID != id1 {
		t.Fatalf("genre Drama via features: total=%d movies=%v", total, movies)
	}
	// Multi-value OR: "剧情|Comedy" splits via splitTerms.
	_, total, err = s.search(0, lib, nil, "", "success", "", "剧情|Comedy", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("genre 剧情|Comedy total=%d want 1", total)
	}
	// Tag / studio kinds.
	_, total, err = s.search(0, lib, nil, "", "success", "", "", "单体", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("tag 单体 total=%d want 1", total)
	}
	_, total, err = s.search(0, lib, nil, "", "success", "", "", "", "Studio X", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("studio Studio X total=%d want 1", total)
	}

	// Update trigger: new title searchable, old title gone.
	if _, err := s.db.Exec("UPDATE movies SET title='Changed' WHERE id=?", id1); err != nil {
		t.Fatal(err)
	}
	if got := ftsSearchCount(t, s, "hang"); got != 1 {
		t.Fatalf("after update Changed: %d", got)
	}
	if got := ftsSearchCount(t, s, "Hello"); got != 0 {
		t.Fatalf("after update stale Hello: %d", got)
	}

	// Delete trigger: row gone from FTS.
	if _, err := s.db.Exec("DELETE FROM movies WHERE id=?", id1); err != nil {
		t.Fatal(err)
	}
	if got := ftsSearchCount(t, s, "hang"); got != 0 {
		t.Fatalf("after delete hang: %d", got)
	}
	if got := ftsSearchCount(t, s, "ABP"); got != 0 {
		t.Fatalf("after delete ABP: %d", got)
	}
}

// TestFtsMigrationReapplies verifies deleting ledger version 22 and
// reopening rebuilds movies_fts + triggers and backfills existing rows.
func TestFtsMigrationReapplies(t *testing.T) {
	path := t.TempDir() + "/fts-reapply.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := s.AddLibrary("AV", "/tmp/av")
	if err != nil {
		t.Fatal(err)
	}
	upsertFixtureMovie(t, s, lib.ID, Movie{
		LibraryID: lib.ID, SourcePath: "/tmp/av/fts-reapply.strm", Status: "success",
		Title: "Reapply Title", Number: "XYZ-1",
	})
	s.waitFeaturesReady()
	if got := ftsSearchCount(t, s, "eapp"); got != 1 {
		t.Fatalf("pre-drop Reapply: %d", got)
	}
	// Drop FTS objects + ledger row to simulate pre-22 database.
	for _, stmt := range []string{
		"DROP TRIGGER movies_fts_insert",
		"DROP TRIGGER movies_fts_update",
		"DROP TRIGGER movies_fts_delete",
		"DROP TABLE movies_fts",
		"DELETE FROM schema_migrations WHERE version=22",
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.waitFeaturesReady()
	if got := ftsSearchCount(t, reopened, "eapp"); got != 1 {
		t.Fatalf("after reapply Reapply: %d", got)
	}
	// Writes after reapply keep FTS in sync via recreated triggers.
	var id int64
	if err := reopened.db.QueryRow("SELECT id FROM movies WHERE source_path='/tmp/av/fts-reapply.strm'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.Exec("UPDATE movies SET title='Reapplied' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if got := ftsSearchCount(t, reopened, "ppli"); got != 1 {
		t.Fatalf("after reapply update Reapplied: %d", got)
	}
}

func ftsSearchCount(t *testing.T, s *Store, term string) int {
	t.Helper()
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM movies_fts WHERE movies_fts MATCH ?", `"`+term+`"`).Scan(&count)
	if err != nil {
		// FTS MATCH with special chars may error; treat as 0 for negative cases
		// but fail fast on unexpected errors for positive fixtures.
		t.Logf("fts match %q err=%v", term, err)
		return -1
	}
	return count
}

// TestSearchTermFtsIntegration exercises search() end-to-end for long vs
// short terms and entity filters.
func TestSearchTermFtsIntegration(t *testing.T) {
	s, lib := newFeatureStore(t)
	upsertFixtureMovie(t, s, lib, Movie{
		LibraryID: lib, SourcePath: "/tmp/av/fts-int1.strm", Status: "success",
		Title: "中文标题测试", Number: "ABP-123", Genres: []string{"剧情"},
	})
	upsertFixtureMovie(t, s, lib, Movie{
		LibraryID: lib, SourcePath: "/tmp/av/fts-int2.strm", Status: "success",
		Title: "Hello World", Number: "XYZ-9",
	})
	s.waitFeaturesReady()

	for _, tc := range []struct {
		term string
		want int
	}{
		{"标题测", 1},   // 3-rune trigram substring
		{"中文", -1},   // 2 runes -> LIKE fallback, len is checked via total below
		{"Hello", 1}, // trigram
		{"hello", 1}, // case-insensitive trigram? FTS is case-insensitive for ASCII
		{"ABP", 1},   // number column trigram
		{"测试", -1},   // 2 runes LIKE fallback
		{"missing", 0},
	} {
		_, total, err := s.search(0, lib, nil, tc.term, "success", "", "", "", "", "", "", "", false, false, "title", false, 10, 0, "")
		if err != nil {
			t.Fatalf("term %q: %v", tc.term, err)
		}
		if tc.want >= 0 && total != tc.want {
			t.Errorf("term %q total=%d want %d", tc.term, total, tc.want)
		}
		if tc.want < 0 {
			// short-term fallback: verify LIKE finds the fixture where expected
			if tc.term == "中文" && total != 1 {
				t.Errorf("short term %q LIKE fallback total=%d want 1", tc.term, total)
			}
			if tc.term == "测试" && total != 1 {
				t.Errorf("short term %q LIKE fallback total=%d want 1", tc.term, total)
			}
		}
	}

	// Genre filter via features still works alongside FTS term.
	_, total, err := s.search(0, lib, nil, "标题", "success", "", "剧情", "", "", "", "", "", false, false, "title", false, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	// "标题" is 2 runes -> LIKE fallback, intersects genre.
	if total != 1 {
		t.Fatalf("term+genre total=%d want 1", total)
	}
	fmt.Println("fts integration done")
}
