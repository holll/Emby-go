package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These are exactly the schema-nullable columns newly defaulted by movieCols.
var nullableMovieMetadata = []struct{ column, field string }{
	{"file_mtime", ""}, {"source_protocol", "SourceProtocol"}, {"source_container", "SourceContainer"},
	{"number", "Number"}, {"nfo_path", "NFOPath"}, {"output_dir", "OutputDir"}, {"title", "Title"},
	{"original_title", "OriginalTitle"}, {"plot", "Plot"}, {"year", "Year"}, {"premiered", "Premiere"},
	{"rating", "Rating"}, {"director", "Director"}, {"series", "Series"}, {"maker", "Maker"}, {"label", "Label"},
	{"genres", "Genres"}, {"tags", "Tags"}, {"studios", "Studios"}, {"poster_path", "PosterPath"},
	{"backdrop_path", "BackdropPath"}, {"runtime_seconds", "RuntimeSeconds"},
}

func nullMetadataVersions(t *testing.T, s *Store) string {
	t.Helper()
	// Open's independent initialization must finish before the read-only snapshot.
	s.waitFeaturesReady()
	rows, err := s.db.Query("SELECT key,value FROM kv ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%q=%q;", key, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var schema, migrations int
	if err := s.db.QueryRow("SELECT MAX(version),COUNT(*) FROM schema_migrations").Scan(&schema, &migrations); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&out, "schema=%d;migrations=%d;published=%s", schema, migrations, s.Version("g:version"))
	return out.String()
}

func TestNullMetadataWideProjections(t *testing.T) {
	for i := 0; i <= len(nullableMovieMetadata); i++ {
		columns := nullableMovieMetadata
		name := "all"
		if i < len(columns) {
			columns = columns[i : i+1]
			name = columns[0].column
		}
		t.Run(name, func(t *testing.T) {
			s, lib := newFeatureStore(t)
			id := upsertFixtureMovie(t, s, lib, Movie{
				LibraryID: lib, SourcePath: "/tmp/null-wide.strm", Status: "success",
				SourceProtocol: "http", SourceContainer: "mkv", Number: "N1", NFOPath: "movie.nfo", OutputDir: "out",
				Title: "Title", OriginalTitle: "Original", Plot: "Plot", Year: 2024, Premiere: "2024-01-01", Rating: 8.5,
				Director: "Director", Series: "Series", Maker: "Maker", Label: "Label", Collection: "Collection",
				Genres: []string{"Drama"}, Tags: []string{"Tag"}, Studios: []string{"Studio"}, PosterPath: "poster.jpg",
				BackdropPath: "backdrop.jpg", RuntimeSeconds: 120,
			})
			if err := s.ReplaceActors(id, []ActorRef{{Name: "Actor"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("INSERT INTO userdata(user_id,movie_id,position_ticks) VALUES(1,?,10000000)", id); err != nil {
				t.Fatal(err)
			}
			want, err := s.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			var assignments []string
			for _, c := range columns {
				assignments = append(assignments, c.column+"=NULL")
				if c.field != "" {
					reflect.ValueOf(&want).Elem().FieldByName(c.field).SetZero()
				}
			}
			if _, err := s.db.Exec("UPDATE movies SET "+strings.Join(assignments, ",")+" WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
			before := nullMetadataVersions(t, s)
			queries := map[string]func() ([]Movie, error){
				"Movie": func() ([]Movie, error) { m, e := s.Movie(id); return []Movie{m}, e },
				"IDs":   func() ([]Movie, error) { m, e := s.MoviesByIDs([]int64{id}); return []Movie{m[id]}, e },
				"search": func() ([]Movie, error) {
					m, n, e := s.SearchAll(lib, "", "", "title", false, 10, 0)
					if e == nil && n != 1 {
						t.Errorf("search total=%d", n)
					}
					return m, e
				},
				"latest":        func() ([]Movie, error) { return s.Latest(lib, 10, 0) },
				"latestVisible": func() ([]Movie, error) { return s.LatestVisible(0, []int64{lib}, 10, 0) },
				"resumed": func() ([]Movie, error) {
					m, n, e := s.Resumed(1, 10, 0)
					if e == nil && n != 1 {
						t.Errorf("resumed total=%d", n)
					}
					return m, e
				},
				"resumedVisible": func() ([]Movie, error) { m, _, e := s.ResumedVisible(1, []int64{lib}, 10, 0); return m, e },
				"scrape":         func() ([]Movie, error) { return s.MoviesForScrape(lib, false, 0) },
				"scrapeBatch":    func() ([]Movie, error) { return s.MoviesForScrapeBatch(lib, false, 0, 10) },
				"actor":          func() ([]Movie, error) { return s.MoviesByActor("Actor") },
			}
			for name, query := range queries {
				t.Run(name, func(t *testing.T) {
					got, err := query()
					if err != nil || len(got) != 1 {
						t.Fatalf("movies=%+v err=%v", got, err)
					}
					if !reflect.DeepEqual(got[0], want) {
						t.Fatalf("got=%+v\nwant=%+v", got[0], want)
					}
				})
			}
			if after := nullMetadataVersions(t, s); after != before {
				t.Fatalf("read changed versions: %s -> %s", before, after)
			}
			for _, c := range columns {
				var raw any
				if err := s.db.QueryRow("SELECT "+c.column+" FROM movies WHERE id=?", id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if raw != nil {
					t.Fatalf("%s rewritten to %v", c.column, raw)
				}
			}
		})
	}
}

func TestNullMetadataNarrowProjections(t *testing.T) {
	s, lib := newFeatureStore(t)
	id := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/null-narrow.strm", Status: "success", NFOPath: "movie.nfo", Collection: "Collection"})
	if _, err := s.db.Exec("UPDATE movies SET title=NULL,genres=NULL,tags=NULL,studios=NULL,poster_path=NULL WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	before := nullMetadataVersions(t, s)
	probe, err := s.MoviesForProbe(lib, "success", 0)
	if err != nil || len(probe) != 1 || probe[0].Title != "" || probe[0].ID != id {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	for name, query := range map[string]func(int64, string) ([]string, error){"genres": s.Genres, "tags": s.Tags, "studios": s.Studios} {
		got, err := query(lib, "Collection")
		if err != nil || len(got) != 0 {
			t.Fatalf("%s=%v err=%v", name, got, err)
		}
	}
	stats, err := s.CollectionStats(1)
	if err != nil || stats["Collection"].Count != 1 || stats["Collection"].Poster != "" || len(stats["Collection"].Genres) != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	visible, err := s.CollectionStatsVisible(1, lib, []int64{lib})
	if err != nil || visible["Collection"].Count != 1 || visible["Collection"].Poster != "" || len(visible["Collection"].Genres) != 0 {
		t.Fatalf("visible=%+v err=%v", visible, err)
	}
	count, genres, err := s.CollectionSummary("Collection")
	if err != nil || count != 1 || len(genres) != 0 {
		t.Fatalf("summary=%d,%v,%v", count, genres, err)
	}
	if nullMetadataVersions(t, s) != before {
		t.Fatal("narrow reads changed versions")
	}
}

func TestNullMetadataNumericErrorsAndRequiredConstraints(t *testing.T) {
	for _, column := range []string{"year", "rating", "runtime_seconds"} {
		t.Run(column, func(t *testing.T) {
			s, lib := newFeatureStore(t)
			id := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/bad-number.strm", Status: "success"})
			if _, err := s.db.Exec("UPDATE movies SET "+column+"='not-a-number' WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
			before := nullMetadataVersions(t, s)
			if _, err := s.Movie(id); err == nil {
				t.Fatal("bad non-NULL numeric value accepted")
			}
			if _, err := s.MoviesForScrape(lib, false, 0); err == nil {
				t.Fatal("scrape swallowed numeric scan error")
			}
			if column == "runtime_seconds" {
				for _, op := range indexedUserOperations() {
					if err := op.write(s, id); err == nil {
						t.Errorf("%s swallowed runtime scan error", op.name)
					}
				}
			}
			if nullMetadataVersions(t, s) != before {
				t.Fatal("failed reads/writes changed versions")
			}
		})
	}
	s, lib := newFeatureStore(t)
	id := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/required.strm", Status: "success"})
	for _, column := range []string{"library_id", "source_path", "status", "additional_parts", "created_at", "updated_at"} {
		if _, err := s.db.Exec("UPDATE movies SET "+column+"=NULL WHERE id=?", id); err == nil {
			t.Errorf("required column %s accepted NULL", column)
		}
	}
	// The id projection must remain raw, not silently defaulted like metadata.
	if strings.HasPrefix(movieCols, "COALESCE(id") || !strings.HasPrefix(movieCols, "id,library_id,source_path,") {
		t.Fatal("required identity projection changed")
	}
}

func TestNullMetadataPreservesRawFilteringOrderingAndJSON(t *testing.T) {
	s, lib := newFeatureStore(t)
	// Insertion order is deliberately the reverse of SQLite's NULL-before-empty order.
	emptyID := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/empty.strm", Status: "success", NFOPath: "empty.nfo"})
	nullID := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/null.strm", Status: "success", NFOPath: "null.nfo"})
	if _, err := s.db.Exec("UPDATE movies SET title=NULL,year=NULL,nfo_path=NULL,genres='broken-json',tags='[1]',studios='{}' WHERE id=?", nullID); err != nil {
		t.Fatal(err)
	}
	before := nullMetadataVersions(t, s)
	for _, desc := range []bool{false, true} {
		movies, count, err := s.SearchAll(lib, "", "", "title", desc, 10, 0)
		if err != nil || count != 2 {
			t.Fatalf("search count=%d err=%v", count, err)
		}
		want := []int64{nullID, emptyID}
		if desc {
			want = []int64{emptyID, nullID}
		}
		assertScrapeMovieIDs(t, movies, want, "raw title order")
	}
	movies, count, err := s.SearchFiltered(1, lib, "", "0", "", false, "title", false, 10, 0)
	if err != nil || count != 1 {
		t.Fatalf("year=0 filter count=%d err=%v", count, err)
	}
	assertScrapeMovieIDs(t, movies, []int64{emptyID}, "NULL year must not match zero")
	probe, err := s.MoviesForProbe(lib, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertScrapeMovieIDs(t, probe, []int64{emptyID}, "NULL NFO stays excluded")
	movie, err := s.Movie(nullID)
	if err != nil {
		t.Fatal(err)
	}
	// parseStrings already ignores malformed JSON; retain that contract, including
	// its partial result for a wrong element type, rather than adding recovery.
	if !reflect.DeepEqual(movie.Genres, parseStrings("broken-json")) || !reflect.DeepEqual(movie.Tags, parseStrings("[1]")) || !reflect.DeepEqual(movie.Studios, parseStrings("{}")) {
		t.Fatalf("JSON parsing contract changed: %+v", movie)
	}
	var rawGenres, rawTags, rawStudios string
	if err := s.db.QueryRow("SELECT genres,tags,studios FROM movies WHERE id=?", nullID).Scan(&rawGenres, &rawTags, &rawStudios); err != nil {
		t.Fatal(err)
	}
	if rawGenres != "broken-json" || rawTags != "[1]" || rawStudios != "{}" || nullMetadataVersions(t, s) != before {
		t.Fatal("read rewrote malformed JSON or versions")
	}
}

func TestNullRuntimeIndexedUserSnapshots(t *testing.T) {
	operations := []struct {
		name  string
		write func(*Store, int64) (UserData, error)
	}{
		{"favorite", func(s *Store, id int64) (UserData, error) { return s.SetIndexedFavoriteData(id, true) }},
		{"likes", func(s *Store, id int64) (UserData, error) { return s.SetIndexedLikesData(id, -1) }},
		{"hidden", func(s *Store, id int64) (UserData, error) { return s.SetIndexedHideFromResumeData(id, true) }},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			s, lib := newFeatureStore(t)
			id := upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/null-runtime.strm", Status: "success"})
			if _, err := s.db.Exec("UPDATE movies SET runtime_seconds=NULL WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("INSERT INTO userdata(user_id,movie_id,position_ticks) VALUES(1,?,10000000)", id); err != nil {
				t.Fatal(err)
			}
			before := []string{storedVersion(t, s, "g:version"), storedVersion(t, s, fmt.Sprintf("lib:%d:version", lib))}
			data, err := op.write(s, id)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := s.DataUser(1, id)
			if err != nil || stored != data {
				t.Fatalf("response=%+v stored=%+v err=%v", data, stored, err)
			}
			if err != nil || data.PlayedPercentage != 0 || data.PositionTicks != 10000000 {
				t.Fatalf("data=%+v err=%v", data, err)
			}
			if op.name == "favorite" && !data.IsFavorite || op.name == "likes" && data.Likes != -1 || op.name == "hidden" && !data.HideFromResume {
				t.Fatalf("setting missing: %+v", data)
			}
			for i, key := range []string{"g:version", fmt.Sprintf("lib:%d:version", lib)} {
				if storedVersion(t, s, key) == before[i] {
					t.Errorf("%s did not advance", key)
				}
			}
			var raw sql.NullInt64
			if err := s.db.QueryRow("SELECT runtime_seconds FROM movies WHERE id=?", id).Scan(&raw); err != nil || raw.Valid {
				t.Fatalf("runtime rewritten: %+v, %v", raw, err)
			}
		})
	}
}
