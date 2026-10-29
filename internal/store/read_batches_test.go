package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestBatchReadsLargeAndDuplicateIDs(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	f.s.db.SetMaxOpenConns(1)
	if err := f.s.ReplaceActors(f.victim, []ActorRef{{Name: "Z"}, {Name: "A"}}); err != nil {
		t.Fatal(err)
	}
	before := indexSnapshot(t, f.s)
	expectedData, err := f.s.DataForUser(1, []int64{f.victim, f.survivor})
	if err != nil {
		t.Fatal(err)
	}
	expectedActors, err := f.s.ActorsFor([]int64{f.victim, f.survivor})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 40010)
	ids = append(ids, f.victim)
	for i := int64(40000); i >= 1; i-- {
		ids = append(ids, i)
	}
	ids = append(ids, f.victim, f.survivor, f.victim)
	gotData, err := f.s.DataForUser(1, ids)
	if err != nil || !reflect.DeepEqual(gotData, expectedData) {
		t.Fatalf("data=%v err=%v", gotData, err)
	}
	gotActors, err := f.s.ActorsFor(ids)
	if err != nil || !reflect.DeepEqual(gotActors, expectedActors) {
		t.Fatalf("actors=%v err=%v", gotActors, err)
	}
	if names := gotActors[f.victim]; len(names) != 2 || names[0].Name != "A" || names[1].Name != "Z" {
		t.Fatalf("order/duplicates=%v", names)
	}
	if indexSnapshot(t, f.s) != before {
		t.Fatal("read changed business/version snapshot")
	}
	f.s.Close()
	if d, err := f.s.DataForUser(1, nil); err != nil || d == nil || len(d) != 0 {
		t.Fatalf("empty data=%v err=%v", d, err)
	}
	if a, err := f.s.ActorsFor([]int64{}); err != nil || a == nil || len(a) != 0 {
		t.Fatalf("empty actors=%v err=%v", a, err)
	}
}

func TestBatchReadSharedSnapshotAndBudget(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	if _, err := f.s.db.Exec("INSERT INTO kv(key,value) VALUES('fixture:read','before')"); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	ids = append(ids, ids...)
	calls := 0
	err := f.s.readIDBatches(ids, func(q batchQueryer, marks string, args []any) error {
		calls++
		if len(args) > 500 || len(args) == 0 || len(marks) != len(args)*2-1 {
			t.Fatalf("budget=%d marks=%s", len(args), marks)
		}
		rows, err := q.Query("SELECT value FROM kv WHERE key='fixture:read'")
		if err != nil {
			return err
		}
		if !rows.Next() {
			rows.Close()
			return fmt.Errorf("missing value")
		}
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if value != "before" {
			t.Fatalf("mixed snapshots on chunk%d: %s", calls, value)
		}
		if calls == 1 {
			_, err = f.s.db.Exec("UPDATE kv SET value='after' WHERE key='fixture:read'")
			return err
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	var after string
	if err := f.s.db.QueryRow("SELECT value FROM kv WHERE key='fixture:read'").Scan(&after); err != nil || after != "after" {
		t.Fatalf("write not visible: %q %v", after, err)
	}
	calls = 0
	if err := f.s.readIDBatches([]int64{1, 1, 1}, func(q batchQueryer, _ string, args []any) error {
		calls++
		if q != f.s.db || len(args) != 1 {
			t.Fatal("small request lost single-statement path")
		}
		return nil
	}); err != nil || calls != 1 {
		t.Fatalf("small calls=%d err=%v", calls, err)
	}
}

func TestBatchReadLateErrorsReturnNoPartialMap(t *testing.T) {
	for _, actors := range []bool{false, true} {
		t.Run(fmt.Sprint(actors), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			f.s.db.SetMaxOpenConns(1)
			ids := []int64{f.victim}
			for i := range 499 {
				ids = append(ids, int64(100000+i))
			}
			ids = append(ids, f.survivor)
			if actors {
				if _, err := f.s.db.Exec(fmt.Sprintf(`ALTER TABLE movie_actors RENAME TO fixture_original_relations;
CREATE VIEW movie_actors AS SELECT movie_id,CASE WHEN movie_id=%d THEN NULL ELSE actor_name END AS actor_name FROM fixture_original_relations;`, f.survivor)); err != nil {
					t.Fatal(err)
				}
				got, err := f.s.ActorsFor(ids)
				if err == nil || got != nil {
					t.Fatalf("partial actor map=%v err=%v", got, err)
				}
				if _, err := f.s.db.Exec(`DROP VIEW movie_actors; ALTER TABLE fixture_original_relations RENAME TO movie_actors;`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.ActorsFor(ids); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.s.db.Exec("UPDATE userdata SET position_ticks='private-bad-ticks' WHERE movie_id=?", f.survivor); err != nil {
					t.Fatal(err)
				}
				got, err := f.s.DataForUser(1, ids)
				if err == nil || got != nil {
					t.Fatalf("partial data map=%v err=%v", got, err)
				}
				if _, err := f.s.db.Exec("UPDATE userdata SET position_ticks=120 WHERE movie_id=?", f.survivor); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.DataForUser(1, ids); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
