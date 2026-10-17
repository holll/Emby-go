package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestIndexedPlayedValuesAndDomains(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		for _, zero := range []bool{false, true} {
			t.Run(fmt.Sprintf("fresh=%v/zero=%v", fresh, zero), func(t *testing.T) {
				f := newIndexedDeleteFixture(t)
				if zero {
					if _, err := f.s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/fixture/zero')"); err != nil {
						t.Fatal(err)
					}
					if _, err := f.s.db.Exec("UPDATE movies SET library_id=0 WHERE id=?", f.victim); err != nil {
						t.Fatal(err)
					}
					if err := f.s.BumpVersion(0); err != nil {
						t.Fatal(err)
					}
					f.other = f.lib
					f.lib = 0
				}
				if fresh {
					if _, err := f.s.db.Exec("DELETE FROM userdata WHERE movie_id=?", f.victim); err != nil {
						t.Fatal(err)
					}
				}
				expected, err := f.s.Data(f.victim)
				if err != nil {
					t.Fatal(err)
				}
				if fresh {
					expected.StoppedTicks = -1
				}
				movie, _ := f.s.Movie(f.victim)
				other, _ := f.s.Data(f.survivor)
				for _, played := range []bool{true, true, false, false} {
					versions := indexedDeleteVersions(t, f)
					if err := f.s.SetIndexedPlayed(f.victim, played); err != nil {
						t.Fatal(err)
					}
					expected.Played = played
					got, err := f.s.Data(f.victim)
					if err != nil || got != expected {
						t.Fatalf("got=%+v expected=%+v err=%v", got, expected, err)
					}
					assertIndexedDeleteVersions(t, f, versions, true)
					current, _ := f.s.Movie(f.victim)
					data, _ := f.s.Data(f.survivor)
					if !reflect.DeepEqual(movie, current) || data != other {
						t.Fatal("unrelated state changed")
					}
				}
				snapshot := indexedDeleteVersions(t, f)
				if err := f.s.SetPlayed(f.victim, true); err != nil {
					t.Fatal(err)
				}
				assertIndexedDeleteVersions(t, f, snapshot, false)
			})
		}
	}
}

func TestIndexedPlayedRejectsMissingOrHidden(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprint(hidden), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			id := int64(999999)
			if hidden {
				id = f.victim
				if _, err := f.s.db.Exec("UPDATE movies SET status='pending' WHERE id=?", id); err != nil {
					t.Fatal(err)
				}
			}
			before := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			if err := f.s.SetIndexedPlayed(id, true); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("error=%v", err)
			}
			if indexSnapshot(t, f.s) != before {
				t.Fatal("rejected write changed snapshot")
			}
			assertIndexedDeleteVersions(t, f, versions, false)
		})
	}
}

func TestIndexedPlayedRollbackAndPoolHealth(t *testing.T) {
	for _, stage := range []string{"global", "library", "insert", "update", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			f.s.db.SetMaxOpenConns(1)
			if stage == "commit" {
				if _, err := f.s.db.Exec(`CREATE TABLE played_parent(id INTEGER PRIMARY KEY);
CREATE TABLE played_child(id INTEGER REFERENCES played_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_played AFTER UPDATE ON userdata BEGIN INSERT INTO played_child VALUES(999); END;`); err != nil {
					t.Fatal(err)
				}
			} else {
				clause := map[string]string{"global": "BEFORE UPDATE ON kv WHEN OLD.key='g:version'", "library": fmt.Sprintf("BEFORE UPDATE ON kv WHEN OLD.key='lib:%d:version'", f.lib), "insert": "BEFORE INSERT ON userdata", "update": "BEFORE UPDATE ON userdata"}[stage]
				if _, err := f.s.db.Exec("CREATE TRIGGER fail_played " + clause + " BEGIN SELECT RAISE(ABORT,'played-private-failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			before := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			if err := f.s.SetIndexedPlayed(f.victim, true); err == nil {
				t.Fatal("injected failure accepted")
			}
			if indexSnapshot(t, f.s) != before {
				t.Fatal("failure changed snapshot")
			}
			assertIndexedDeleteVersions(t, f, versions, false)
			if stage == "commit" && indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM played_child") != 0 {
				t.Fatal("child survived rollback")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.db.Exec("DROP TRIGGER fail_played"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.SetIndexedPlayed(f.victim, true); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteVersions(t, f, versions, true)
		})
	}
}
