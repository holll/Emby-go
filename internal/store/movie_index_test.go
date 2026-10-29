package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func indexSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var snapshot []any
	for _, query := range []string{
		"SELECT * FROM movies ORDER BY id", "SELECT * FROM actors ORDER BY name",
		"SELECT * FROM movie_actors ORDER BY movie_id,actor_name",
		"SELECT * FROM movie_features ORDER BY movie_id,kind,value",
		"SELECT * FROM userdata ORDER BY movie_id",
		"SELECT key,value FROM kv WHERE key='g:version' OR key LIKE 'lib:%:version' ORDER BY key",
	} {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot = append(snapshot, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot = append(snapshot, s.Version("g:version"))
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestIndexMovieRollsBackEveryStage(t *testing.T) {
	faults := []struct{ name, trigger string }{
		{"global", `BEFORE INSERT ON kv WHEN NEW.key='g:version'`},
		{"library", `BEFORE INSERT ON kv WHEN NEW.key LIKE 'lib:%:version'`},
		{"movie", `BEFORE INSERT ON movies`},
		{"actors", `BEFORE INSERT ON movie_actors WHEN NEW.actor_name='New'`},
		{"features", `BEFORE INSERT ON movie_features`},
	}
	for _, exists := range []bool{false, true} {
		for _, fault := range faults {
			t.Run(fmt.Sprintf("%t/%s", exists, fault.name), func(t *testing.T) {
				s, lib := newFeatureStore(t)
				s.waitFeaturesReady()
				m := Movie{LibraryID: lib, SourcePath: "/fixture/source.strm", Title: "Old", Status: "success", Genres: []string{"OldGenre"}}
				if exists {
					id, err := s.IndexMovie(m, 10, time.Unix(1, 0), []ActorRef{{Name: "Old", AvatarURL: "old-avatar"}})
					if err != nil {
						t.Fatal(err)
					}
					if err := s.SavePlaybackForUser(1, id, 120, 3, "fixture-time", 99); err != nil {
						t.Fatal(err)
					}
				}
				before := indexSnapshot(t, s)
				if _, err := s.db.Exec("CREATE TRIGGER fail_index " + fault.trigger + " BEGIN SELECT RAISE(ABORT,'fixture-index-failure'); END"); err != nil {
					t.Fatal(err)
				}
				m.Title, m.Genres = "New", []string{"NewGenre"}
				id, err := s.IndexMovie(m, 20, time.Unix(2, 0), []ActorRef{{Name: "New", AvatarURL: "new-avatar"}})
				if err == nil || !strings.Contains(err.Error(), "fixture-index-failure") || id != 0 {
					t.Fatalf("id=%d err=%v", id, err)
				}
				if after := indexSnapshot(t, s); after != before {
					t.Fatalf("failed %s changed committed index\nbefore=%s\nafter=%s", fault.name, before, after)
				}
				if _, err := s.db.Exec("DROP TRIGGER fail_index"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.IndexMovie(m, 20, time.Unix(2, 0), []ActorRef{{Name: "New"}}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestIndexMoviePreservesIdentityPlaybackAndActorRules(t *testing.T) {
	s, lib := newFeatureStore(t)
	s.waitFeaturesReady()
	m := Movie{LibraryID: lib, SourcePath: "/fixture/source.strm", Status: "manual", Title: "Old", Genres: []string{"OldGenre"}}
	id, err := s.IndexMovie(m, 1, time.Unix(1, 0), []ActorRef{{Name: " Actor ", AvatarURL: " avatar "}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlaybackForUser(1, id, 120, 3, "fixture-time", 99); err != nil {
		t.Fatal(err)
	}
	m.Title, m.Genres = "New", []string{"NewGenre"}
	updated, err := s.IndexMovie(m, 2, time.Unix(2, 0), []ActorRef{{Name: "Actor"}, {Name: "Actor"}, {Name: " "}})
	if err != nil || updated != id {
		t.Fatalf("id changed: %d -> %d err=%v", id, updated, err)
	}
	got, err := s.Movie(id)
	if err != nil || got.Title != "New" || got.Status != "manual" {
		t.Fatalf("movie=%+v err=%v", got, err)
	}
	actors, err := s.Actors(id)
	if err != nil || len(actors) != 1 || actors[0].Name != "Actor" || actors[0].AvatarURL != "avatar" {
		t.Fatalf("actors=%+v err=%v", actors, err)
	}
	var position, plays int64
	if err := s.db.QueryRow("SELECT position_ticks,play_count FROM userdata WHERE movie_id=?", id).Scan(&position, &plays); err != nil || position != 120 || plays != 3 {
		t.Fatalf("playback=%d/%d err=%v", position, plays, err)
	}
	var oldFeatures, currentFeatures int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movie_features WHERE movie_id=? AND kind='genre' AND value='OldGenre'", id).Scan(&oldFeatures); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movie_features WHERE movie_id=? AND ((kind='genre' AND value='NewGenre') OR (kind='actor' AND value='Actor'))", id).Scan(&currentFeatures); err != nil {
		t.Fatal(err)
	}
	if oldFeatures != 0 || currentFeatures != 2 || s.Version("g:version") != "2" {
		t.Fatalf("features=%d/%d version=%s", oldFeatures, currentFeatures, s.Version("g:version"))
	}
	if _, err := s.IndexMovie(m, 2, time.Unix(2, 0), nil); err != nil {
		t.Fatal(err)
	}
	actors, err = s.Actors(id)
	if err != nil || len(actors) != 0 {
		t.Fatalf("nil replacement must clear relations: %+v %v", actors, err)
	}
}

func TestIndexMovieMoveVersionsBothLibraries(t *testing.T) {
	s, lib := newFeatureStore(t)
	s.waitFeaturesReady()
	other, err := s.AddLibrary("Other", "/fixture/other")
	if err != nil {
		t.Fatal(err)
	}
	m := Movie{LibraryID: lib, SourcePath: "/fixture/source.strm", Status: "success", Title: "Old"}
	id, err := s.IndexMovie(m, 0, time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	before := indexSnapshot(t, s)
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_old_library BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'fixture-old-library'); END`, lib)); err != nil {
		t.Fatal(err)
	}
	m.LibraryID = other.ID
	if _, err := s.IndexMovie(m, 0, time.Unix(2, 0), nil); err == nil {
		t.Fatal("old library invalidation failure ignored")
	}
	if indexSnapshot(t, s) != before {
		t.Fatal("failed move changed index or version")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_old_library"); err != nil {
		t.Fatal(err)
	}
	moved, err := s.IndexMovie(m, 0, time.Unix(2, 0), nil)
	if err != nil || moved != id {
		t.Fatalf("move=%d err=%v", moved, err)
	}
	if s.Version("g:version") != "2" || storedVersion(t, s, fmt.Sprintf("lib:%d:version", lib)) != "2" || storedVersion(t, s, fmt.Sprintf("lib:%d:version", other.ID)) != "1" {
		t.Fatal("move did not invalidate both libraries")
	}
}

func TestIndexMovieCommitFailureRollsBackAll(t *testing.T) {
	s, lib := newFeatureStore(t)
	s.waitFeaturesReady()
	s.db.SetMaxOpenConns(1)
	m := Movie{LibraryID: lib, SourcePath: "/fixture/source.strm", Title: "Old", Status: "success", Genres: []string{"OldGenre"}}
	if _, err := s.IndexMovie(m, 1, time.Unix(1, 0), []ActorRef{{Name: "Old"}}); err != nil {
		t.Fatal(err)
	}
	before := indexSnapshot(t, s)
	_, err := s.db.Exec(`CREATE TABLE fixture_index_parent(id INTEGER PRIMARY KEY);
		CREATE TABLE fixture_index_child(parent_id INTEGER REFERENCES fixture_index_parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_index_commit AFTER INSERT ON movie_actors WHEN NEW.actor_name='New'
		BEGIN INSERT INTO fixture_index_child(parent_id) VALUES(999); END`)
	if err != nil {
		t.Fatal(err)
	}
	m.Title, m.Genres = "New", []string{"NewGenre"}
	id, err := s.IndexMovie(m, 2, time.Unix(2, 0), []ActorRef{{Name: "New"}})
	if id != 0 || err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected deferred commit failure: id=%d err=%v", id, err)
	}
	if indexSnapshot(t, s) != before {
		t.Fatal("commit failure changed metadata, relations, features or versions")
	}
	var children int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM fixture_index_child").Scan(&children); err != nil || children != 0 {
		t.Fatalf("commit failure retained fixture writes: %d %v", children, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_index_commit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IndexMovie(m, 2, time.Unix(2, 0), []ActorRef{{Name: "New"}}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexMovieConcurrentWritersStayWhole(t *testing.T) {
	s, lib := newFeatureStore(t)
	s.waitFeaturesReady()
	const writers = 16
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for n := range writers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("Actor-%d", n)
			_, err := s.IndexMovie(Movie{LibraryID: lib, SourcePath: "/fixture/source.strm", Title: name, Status: "success", Genres: []string{name}}, 0, time.Unix(int64(n), 0), []ActorRef{{Name: name}})
			errs <- err
		}(n)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.MovieIDByPath("/fixture/source.strm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	actors, err := s.Actors(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 1 || actors[0].Name != m.Title || !slices.Equal(m.Genres, []string{m.Title}) {
		t.Fatalf("mixed commit: movie=%+v actors=%+v", m, actors)
	}
	var actorFeatures int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movie_features WHERE movie_id=? AND kind='actor' AND value=?", id, m.Title).Scan(&actorFeatures); err != nil {
		t.Fatal(err)
	}
	if actorFeatures != 1 || s.Version("g:version") != strconv.Itoa(writers) {
		t.Fatal("features/version disagree with final commit")
	}
}
