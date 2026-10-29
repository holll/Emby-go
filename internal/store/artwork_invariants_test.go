package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func artworkRelatedSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var snapshot []any
	for _, query := range []string{"SELECT * FROM actors ORDER BY name", "SELECT * FROM movie_actors ORDER BY movie_id,actor_name", "SELECT * FROM movie_features ORDER BY movie_id,kind,value", "SELECT * FROM userdata ORDER BY movie_id"} {
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
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestArtworkOnlyInvalidatesDependentScanGroup(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	m, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := f.s.Library(m.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []struct {
		lib       int64
		key, path string
	}{{f.lib, "target", m.SourcePath}, {f.lib, "same-library-unrelated", "/fixture/delete/other.strm"}, {f.other, "other-library", m.SourcePath}} {
		if _, err := f.s.db.Exec("INSERT INTO scan_groups(library_id,group_key,fingerprint_version,fingerprint,outcome) VALUES(?,?,1,'baseline','success')", g.lib, g.key); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.db.Exec("INSERT INTO scan_group_sources(library_id,group_key,source_path,role) VALUES(?,?,?,'standalone')", g.lib, g.key, g.path); err != nil {
			t.Fatal(err)
		}
	}
	beforeRelated := artworkRelatedSnapshot(t, f.s)
	beforeOther, err := f.s.Movie(f.survivor)
	if err != nil {
		t.Fatal(err)
	}
	versions := indexedDeleteVersions(t, f)
	if err := f.s.CompareAndSwapArtwork(context.Background(), m, lib.Path, "/new/private.jpg", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if artworkRelatedSnapshot(t, f.s) != beforeRelated {
		t.Fatal("artwork changed actors/features/userdata")
	}
	afterOther, err := f.s.Movie(f.survivor)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeOther, afterOther) {
		t.Fatal("artwork changed unrelated movie")
	}
	for _, check := range []struct {
		lib   int64
		key   string
		count int
	}{{f.lib, "target", 0}, {f.lib, "same-library-unrelated", 1}, {f.other, "other-library", 1}} {
		if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM scan_groups WHERE library_id=? AND group_key=?", check.lib, check.key); n != check.count {
			t.Fatalf("group %s count=%d", check.key, n)
		}
		if n := indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM scan_group_sources WHERE library_id=? AND group_key=?", check.lib, check.key); n != check.count {
			t.Fatalf("source %s count=%d", check.key, n)
		}
	}
	assertIndexedDeleteVersions(t, f, versions, true)
}
