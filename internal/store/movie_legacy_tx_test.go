package store

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMovieLegacyWrappersKeepVersionsAndCleanFailedCommit(t *testing.T) {
	for _, kind := range []string{"upsert", "actors"} {
		t.Run(kind, func(t *testing.T) {
			s, lib := newFeatureStore(t)
			s.waitFeaturesReady()
			s.db.SetMaxOpenConns(1)
			m := Movie{LibraryID: lib, SourcePath: "/fixture/legacy.strm", Title: "Old", Status: "success"}
			id, err := s.IndexMovie(m, 1, time.Unix(1, 0), []ActorRef{{Name: "Old"}})
			if err != nil {
				t.Fatal(err)
			}
			m.Title = "Fault"
			write := func() error {
				if kind == "actors" {
					return s.ReplaceActors(id, []ActorRef{{Name: "FaultActor"}})
				}
				_, err := s.UpsertMovie(m, 2, time.Unix(2, 0))
				return err
			}
			if err := write(); err != nil {
				t.Fatal(err)
			}
			if s.Version("g:version") != "1" || storedVersion(t, s, "g:version") != "1" || storedVersion(t, s, "lib:"+strconv.FormatInt(lib, 10)+":version") != "1" {
				t.Fatal("legacy wrapper unexpectedly bumped versions")
			}
			before := indexSnapshot(t, s)
			trigger := `AFTER UPDATE ON movies WHEN NEW.title='Fault'`
			if kind == "actors" {
				trigger = `AFTER INSERT ON movie_actors WHEN NEW.actor_name='FaultActor'`
			}
			_, err = s.db.Exec(`CREATE TABLE fixture_legacy_parent(id INTEGER PRIMARY KEY);
				CREATE TABLE fixture_legacy_child(parent_id INTEGER REFERENCES fixture_legacy_parent(id) DEFERRABLE INITIALLY DEFERRED);
				CREATE TRIGGER fail_legacy_commit ` + trigger + ` BEGIN INSERT INTO fixture_legacy_child(parent_id) VALUES(999); END`)
			if err != nil {
				t.Fatal(err)
			}
			if err := write(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				t.Fatalf("expected commit failure: %v", err)
			}
			if indexSnapshot(t, s) != before {
				t.Fatal("legacy failed commit persisted partial index or versions")
			}
			var children int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM fixture_legacy_child").Scan(&children); err != nil || children != 0 {
				t.Fatalf("fixture not rolled back: %d %v", children, err)
			}
			if _, err := s.db.Exec("DROP TRIGGER fail_legacy_commit"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.IndexMovie(m, 2, time.Unix(2, 0), nil); err != nil {
				t.Fatalf("legacy wrapper poisoned next IndexMovie: %v", err)
			}
		})
	}
}
