package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

var snapshotSettings = []struct {
	name  string
	set   func(*Store, int64) (UserData, error)
	check func(UserData) bool
}{
	{"favorite", func(s *Store, id int64) (UserData, error) { return s.SetIndexedFavoriteData(id, true) }, func(d UserData) bool { return d.IsFavorite }},
	{"likes", func(s *Store, id int64) (UserData, error) { return s.SetIndexedLikesData(id, -1) }, func(d UserData) bool { return d.Likes == -1 }},
	{"hidden", func(s *Store, id int64) (UserData, error) { return s.SetIndexedHideFromResumeData(id, true) }, func(d UserData) bool { return d.HideFromResume }},
}

func TestUserDataSnapshotsCommittedAndDetached(t *testing.T) {
	for _, op := range snapshotSettings {
		t.Run(op.name, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			if _, err := f.s.db.Exec("UPDATE movies SET runtime_seconds=120 WHERE id=?; UPDATE userdata SET position_ticks=600000000 WHERE movie_id=?", f.victim, f.victim); err != nil {
				t.Fatal(err)
			}
			before := indexedDeleteVersions(t, f)
			got, err := op.set(f.s, f.victim)
			if err != nil {
				t.Fatal(err)
			}
			if !op.check(got) || got.PositionTicks != 600000000 || got.PlayCount != 3 || got.LastPlayedAt != "fixture-time" || got.StoppedTicks != 99 || got.PlayedPercentage != 50 {
				t.Fatalf("snapshot=%+v", got)
			}
			persisted, err := f.s.Data(f.victim)
			if err != nil {
				t.Fatal(err)
			}
			persisted.PlayedPercentage = 50
			if got != persisted {
				t.Fatalf("returned=%+v persisted=%+v", got, persisted)
			}
			assertIndexedDeleteVersions(t, f, before, true)
			copy := got
			if _, err = f.s.db.Exec("UPDATE userdata SET position_ticks=1,play_count=999,likes=1 WHERE movie_id=?", f.victim); err != nil {
				t.Fatal(err)
			}
			if err = f.s.DeleteIndexedMovie(f.victim); err != nil {
				t.Fatal(err)
			}
			if got != copy || !op.check(got) || got.PlayedPercentage != 50 {
				t.Fatal("returned value changed after later write/delete")
			}
		})
	}
}

func TestUserDataSnapshotsStrictReadAndCommitRollback(t *testing.T) {
	for _, op := range snapshotSettings {
		for _, insert := range []bool{false, true} {
			for _, fault := range []string{"scan", "missing", "commit"} {
				t.Run(fmt.Sprintf("%s/insert_%t/%s", op.name, insert, fault), func(t *testing.T) {
					f := newIndexedDeleteFixture(t)
					f.s.db.SetMaxOpenConns(1)
					if insert {
						if _, err := f.s.db.Exec("DELETE FROM userdata WHERE movie_id=?", f.victim); err != nil {
							t.Fatal(err)
						}
					}
					before := indexSnapshot(t, f.s)
					event := "UPDATE"
					if insert {
						event = "INSERT"
					}
					var action string
					switch fault {
					case "scan":
						action = "UPDATE userdata SET position_ticks='not-an-integer' WHERE movie_id=NEW.movie_id;"
					case "missing":
						action = "DELETE FROM userdata WHERE movie_id=NEW.movie_id;"
					case "commit":
						if _, err := f.s.db.Exec("CREATE TABLE snapshot_parent(id INTEGER PRIMARY KEY); CREATE TABLE snapshot_child(id INTEGER REFERENCES snapshot_parent(id) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
							t.Fatal(err)
						}
						action = "INSERT INTO snapshot_child VALUES(999);"
					}
					if _, err := f.s.db.Exec("CREATE TRIGGER fail_snapshot AFTER " + event + " ON userdata BEGIN " + action + " END"); err != nil {
						t.Fatal(err)
					}
					got, err := op.set(f.s, f.victim)
					if err == nil || got != (UserData{}) {
						t.Fatalf("failure returned data=%+v err=%v", got, err)
					}
					if fault == "missing" && !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("missing row err=%v", err)
					}
					if fault == "commit" && !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
						t.Fatalf("not a deferred COMMIT failure: %v", err)
					}
					if indexSnapshot(t, f.s) != before {
						t.Fatal("response/commit failure did not roll back complete snapshot and mirror")
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					tx, err := f.s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('snapshot:health','ok')"); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
					if fault == "commit" && indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM snapshot_child") != 0 {
						t.Fatal("commit failure left deferred child")
					}
					if _, err = f.s.db.Exec("DROP TRIGGER fail_snapshot"); err != nil {
						t.Fatal(err)
					}
					retry, err := op.set(f.s, f.victim)
					if err != nil || !op.check(retry) {
						t.Fatalf("retry=%+v err=%v", retry, err)
					}
				})
			}
		}
	}
}

func TestUserDataSnapshotsRuntimeAndLegacyMissingDefaults(t *testing.T) {
	for _, runtime := range []int64{0, -1, 120, math.MaxInt64} {
		t.Run(fmt.Sprint(runtime), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			if _, err := f.s.db.Exec("UPDATE movies SET runtime_seconds=? WHERE id=?", runtime, f.victim); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.db.Exec("UPDATE userdata SET position_ticks=600000000 WHERE movie_id=?", f.victim); err != nil {
				t.Fatal(err)
			}
			got, err := f.s.SetIndexedFavoriteData(f.victim, true)
			if err != nil {
				t.Fatal(err)
			}
			want := float64(0)
			if runtime > 0 {
				want = 600000000 / (float64(runtime) * 10000000) * 100
			}
			if got.PlayedPercentage != want || math.IsNaN(got.PlayedPercentage) || math.IsInf(got.PlayedPercentage, 0) {
				t.Fatalf("percentage=%v want=%v", got.PlayedPercentage, want)
			}
		})
	}
	f := newIndexedDeleteFixture(t)
	missing, err := f.s.Data(-999)
	if err != nil || missing != (UserData{}) {
		t.Fatalf("legacy missing=%+v err=%v", missing, err)
	}
	if _, err := readUserData(f.s.db, 1, -999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("strict missing=%v", err)
	}
	for _, op := range snapshotSettings {
		before := indexSnapshot(t, f.s)
		got, err := op.set(f.s, -999)
		if !errors.Is(err, sql.ErrNoRows) || got != (UserData{}) || indexSnapshot(t, f.s) != before {
			t.Fatalf("missing %s: %+v %v", op.name, got, err)
		}
	}
}
