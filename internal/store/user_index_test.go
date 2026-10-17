package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type indexedUserOperation struct {
	name  string
	write func(*Store, int64) error
}

func indexedUserOperations() []indexedUserOperation {
	return []indexedUserOperation{
		{"favorite", func(s *Store, id int64) error { return s.SetIndexedFavorite(id, true) }},
		{"likes", func(s *Store, id int64) error { return s.SetIndexedLikes(id, -1) }},
		{"hidden", func(s *Store, id int64) error { return s.SetIndexedHideFromResume(id, true) }},
	}
}

func TestIndexedUserSettingsValuesAndProtection(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprint(fresh), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			movie, _ := f.s.Movie(f.victim)
			survivor, _ := f.s.Movie(f.survivor)
			otherData, _ := f.s.Data(f.survivor)
			if fresh {
				if _, err := f.s.db.Exec("DELETE FROM userdata WHERE movie_id=?", f.victim); err != nil {
					t.Fatal(err)
				}
			}
			expected, _ := f.s.Data(f.victim)
			if fresh {
				expected.StoppedTicks = -1
			}
			steps := []struct {
				write  func() error
				change func(*UserData)
			}{
				{func() error { return f.s.SetIndexedFavorite(f.victim, true) }, func(d *UserData) { d.IsFavorite = true }},
				{func() error { return f.s.SetIndexedFavorite(f.victim, true) }, func(d *UserData) { d.IsFavorite = true }},
				{func() error { return f.s.SetIndexedFavorite(f.victim, false) }, func(d *UserData) { d.IsFavorite = false }},
				{func() error { return f.s.SetIndexedLikes(f.victim, 1) }, func(d *UserData) { d.Likes = 1 }},
				{func() error { return f.s.SetIndexedLikes(f.victim, -1) }, func(d *UserData) { d.Likes = -1 }},
				{func() error { return f.s.SetIndexedLikes(f.victim, 0) }, func(d *UserData) { d.Likes = 0 }},
				{func() error { return f.s.SetIndexedHideFromResume(f.victim, true) }, func(d *UserData) { d.HideFromResume = true }},
				{func() error { return f.s.SetIndexedHideFromResume(f.victim, false) }, func(d *UserData) { d.HideFromResume = false }},
			}
			for i, step := range steps {
				before := indexedDeleteVersions(t, f)
				if err := step.write(); err != nil {
					t.Fatalf("step%d: %v", i, err)
				}
				step.change(&expected)
				got, err := f.s.Data(f.victim)
				if err != nil || got != expected {
					t.Fatalf("step%d data=%+v want=%+v err=%v", i, got, expected, err)
				}
				assertIndexedDeleteVersions(t, f, before, true)
				current, _ := f.s.Movie(f.victim)
				other, _ := f.s.Movie(f.survivor)
				data, _ := f.s.Data(f.survivor)
				if !reflect.DeepEqual(movie, current) || !reflect.DeepEqual(survivor, other) || data != otherData {
					t.Fatal("unrelated metadata/user data changed")
				}
			}
		})
	}
}

func TestIndexedUserSettingsRejectMissingOrHidden(t *testing.T) {
	for _, op := range indexedUserOperations() {
		for _, hidden := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", op.name, hidden), func(t *testing.T) {
				f := newIndexedDeleteFixture(t)
				id := int64(99999)
				if hidden {
					id = f.victim
					if _, err := f.s.db.Exec("UPDATE movies SET status='pending' WHERE id=?", id); err != nil {
						t.Fatal(err)
					}
				}
				before := indexSnapshot(t, f.s)
				if err := op.write(f.s, id); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("error=%v", err)
				}
				if after := indexSnapshot(t, f.s); after != before {
					t.Fatal("rejected setting changed snapshot/version")
				}
			})
		}
	}
}

func TestIndexedUserSettingsFailureRollback(t *testing.T) {
	for _, op := range indexedUserOperations() {
		for _, stage := range []string{"global", "library", "insert", "update"} {
			t.Run(op.name+"/"+stage, func(t *testing.T) {
				f := newIndexedDeleteFixture(t)
				clause := map[string]string{"global": "BEFORE UPDATE ON kv WHEN OLD.key='g:version'", "library": fmt.Sprintf("BEFORE UPDATE ON kv WHEN OLD.key='lib:%d:version'", f.lib), "insert": "BEFORE INSERT ON userdata", "update": "BEFORE UPDATE ON userdata"}[stage]
				if _, err := f.s.db.Exec("CREATE TRIGGER fail_user " + clause + " BEGIN SELECT RAISE(ABORT,'fixture-user-setting-failure'); END"); err != nil {
					t.Fatal(err)
				}
				before := indexSnapshot(t, f.s)
				versions := indexedDeleteVersions(t, f)
				if err := op.write(f.s, f.victim); err == nil {
					t.Fatal("injected setting failure accepted")
				}
				if after := indexSnapshot(t, f.s); after != before {
					t.Fatal("failure changed business/version snapshot")
				}
				assertIndexedDeleteVersions(t, f, versions, false)
				if _, err := f.s.db.Exec("DROP TRIGGER fail_user"); err != nil {
					t.Fatal(err)
				}
				if err := op.write(f.s, f.victim); err != nil {
					t.Fatal(err)
				}
				assertIndexedDeleteVersions(t, f, versions, true)
			})
		}
	}
}

func TestIndexedUserSettingsCommitFailurePoolHealth(t *testing.T) {
	for _, op := range indexedUserOperations() {
		t.Run(op.name, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			f.s.db.SetMaxOpenConns(1)
			if _, err := f.s.db.Exec(`CREATE TABLE fixture_user_parent(id INTEGER PRIMARY KEY);
CREATE TABLE fixture_user_child(parent_id INTEGER REFERENCES fixture_user_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_user_commit AFTER UPDATE ON userdata BEGIN INSERT INTO fixture_user_child VALUES(987654); END;`); err != nil {
				t.Fatal(err)
			}
			before := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			if err := op.write(f.s, f.victim); err == nil {
				t.Fatal("deferred COMMIT accepted")
			}
			if after := indexSnapshot(t, f.s); after != before {
				t.Fatal("failed COMMIT changed snapshot")
			}
			assertIndexedDeleteVersions(t, f, versions, false)
			if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM fixture_user_child"); n != 0 {
				t.Fatal("deferred child survived rollback")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("connection unusable: %v", err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO fixture_user_parent VALUES(1)"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.db.Exec("DROP TRIGGER fail_user_commit"); err != nil {
				t.Fatal(err)
			}
			if err := op.write(f.s, f.victim); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteVersions(t, f, versions, true)
		})
	}
}

func TestIndexedUserSettingsActualLibrary(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprint(zero), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			oldLib := f.lib
			target := f.other
			if zero {
				target = 0
				if _, err := f.s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/fixture/zero')"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.db.Exec("UPDATE movies SET library_id=? WHERE id=?", target, f.victim); err != nil {
				t.Fatal(err)
			}
			if err := f.s.BumpVersion(target); err != nil {
				t.Fatal(err)
			}
			oldVersion := storedVersion(t, f.s, fmt.Sprintf("lib:%d:version", oldLib))
			f.lib = target
			f.other = oldLib
			before := indexedDeleteVersions(t, f)
			if err := f.s.SetIndexedFavorite(f.victim, true); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteVersions(t, f, before, true)
			if got := storedVersion(t, f.s, fmt.Sprintf("lib:%d:version", oldLib)); got != oldVersion {
				t.Fatal("old library incorrectly bumped")
			}
		})
	}
}

func TestIndexedUserSettingsConcurrentFieldsAndLegacy(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	beforeData, _ := f.s.Data(f.victim)
	before := indexedDeleteVersions(t, f)
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for _, op := range indexedUserOperations() {
		wg.Add(1)
		go func(op indexedUserOperation) { defer wg.Done(); <-start; results <- op.write(f.s, f.victim) }(op)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.s.Data(f.victim)
	beforeData.IsFavorite = true
	beforeData.Likes = -1
	beforeData.HideFromResume = true
	if err != nil || got != beforeData {
		t.Fatalf("concurrent fields=%+v want=%+v err=%v", got, beforeData, err)
	}
	// Each independent explicit setting must advance both domains, not just the last.
	for i, key := range []string{"g:version", fmt.Sprintf("lib:%d:version", f.lib)} {
		var n int
		if _, err := fmt.Sscan(before[i], &n); err != nil {
			t.Fatal(err)
		}
		if value := storedVersion(t, f.s, key); value != fmt.Sprint(n+3) {
			t.Fatalf("%s=%s want=%d", key, value, n+3)
		}
	}
	if storedVersion(t, f.s, fmt.Sprintf("lib:%d:version", f.other)) != before[2] || f.s.Version("g:version") != storedVersion(t, f.s, "g:version") {
		t.Fatal("concurrent version domains/mirror diverged")
	}
	legacyVersion := f.s.Version("g:version")
	for _, err := range []error{f.s.SetFavorite(f.victim, false), f.s.SetLikes(f.victim, 0), f.s.SetHideFromResume(f.victim, false)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.s.Version("g:version") != legacyVersion {
		t.Fatal("legacy setters changed version contract")
	}
}
