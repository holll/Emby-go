package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func imageProtectedSnapshot(t *testing.T, f indexedDeleteFixture) string {
	t.Helper()
	rows, err := f.s.db.Query("SELECT * FROM movies LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot []any
	if err := json.Unmarshal([]byte(indexSnapshot(t, f.s)), &snapshot); err != nil {
		t.Fatal(err)
	}
	var protected []any
	for _, entry := range snapshot[:len(snapshot)-1] {
		row := entry.([]any)
		if len(row) == 2 {
			if key, ok := row[0].(string); ok && (key == "g:version" || strings.HasPrefix(key, "lib:")) {
				continue
			}
		}
		if len(row) == len(columns) && row[0] == float64(f.victim) {
			for i, c := range columns {
				if c == "poster_path" || c == "backdrop_path" || c == "landscape_path" || c == "updated_at" {
					row[i] = nil
				}
			}
		}
		protected = append(protected, row)
	}
	body, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSetIndexedImageOnlyChangesTargetField(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	// These represent a metadata edit that finished after upload's original read.
	if _, err := f.s.db.Exec("UPDATE movies SET title='new concurrent title',plot='new concurrent plot',status='manual',updated_at='2000-01-01T00:00:00Z' WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	protected := imageProtectedSnapshot(t, f)
	var paths [3]string
	for _, step := range []struct {
		kind   string
		column int
	}{{"poster", 0}, {"primary", 0}, {"backdrop", 1}, {"fanart", 1}, {"landscape", 2}, {"landscape", 2}} {
		before := indexedDeleteVersions(t, f)
		paths[step.column] = "/fixture/" + step.kind + ".webp"
		if err := f.s.SetIndexedImage(f.victim, step.kind, paths[step.column]); err != nil {
			t.Fatal(err)
		}
		var got [3]string
		var updated string
		if err := f.s.db.QueryRow("SELECT poster_path,backdrop_path,landscape_path,updated_at FROM movies WHERE id=?", f.victim).Scan(&got[0], &got[1], &got[2], &updated); err != nil {
			t.Fatal(err)
		}
		if got != paths || updated == "2000-01-01T00:00:00Z" {
			t.Fatalf("paths=%v want=%v updated=%s", got, paths, updated)
		}
		assertIndexedDeleteVersions(t, f, before, true)
		if after := imageProtectedSnapshot(t, f); after != protected {
			t.Fatal("image update overwrote metadata, actors, features, playback, source stat, or another movie")
		}
	}
}

func TestSetIndexedImageRollbackAndCommitCleanup(t *testing.T) {
	for _, fault := range []string{"global", "library", "update", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			f.s.db.SetMaxOpenConns(1)
			before := indexSnapshot(t, f.s)
			versions := indexedDeleteVersions(t, f)
			var ddl string
			switch fault {
			case "global":
				ddl = "CREATE TRIGGER fail_image BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'image failure'); END"
			case "library":
				ddl = fmt.Sprintf("CREATE TRIGGER fail_image BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'image failure'); END", f.lib)
			case "update":
				ddl = "CREATE TRIGGER fail_image BEFORE UPDATE ON movies BEGIN SELECT RAISE(ABORT,'image failure'); END"
			case "commit":
				ddl = "CREATE TABLE image_parent(id INTEGER PRIMARY KEY); CREATE TABLE image_child(id INTEGER REFERENCES image_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_image AFTER UPDATE ON movies BEGIN INSERT INTO image_child VALUES(999); END"
			}
			if _, err := f.s.db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			err := f.s.SetIndexedImage(f.victim, "poster", "/fixture/new.webp")
			if err == nil || (fault == "commit" && !strings.Contains(err.Error(), "FOREIGN KEY constraint failed")) {
				t.Fatalf("expected %s failure: %v", fault, err)
			}
			if after := indexSnapshot(t, f.s); after != before {
				t.Fatal("failed image write changed complete snapshot")
			}
			assertIndexedDeleteVersions(t, f, versions, false)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('image:health','ok')"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if fault == "commit" && indexedDeleteCount(t, f.s, "SELECT COUNT(*) FROM image_child") != 0 {
				t.Fatal("deferred child survived")
			}
			if _, err = f.s.db.Exec("DROP TRIGGER fail_image"); err != nil {
				t.Fatal(err)
			}
			if err = f.s.SetIndexedImage(f.victim, "poster", "/fixture/new.webp"); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteVersions(t, f, versions, true)
		})
	}
}

func TestSetIndexedImageMissingAndInvalidKindDoNotMutate(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	for _, kind := range []string{"", "poster_path", "poster_path = ''; DELETE FROM movies; --"} {
		before := indexSnapshot(t, f.s)
		if err := f.s.SetIndexedImage(f.victim, kind, "/fixture/new.webp"); err == nil {
			t.Fatal("accepted invalid kind")
		}
		if indexSnapshot(t, f.s) != before {
			t.Fatal("invalid kind mutated store")
		}
	}
	if err := f.s.DeleteIndexedMovie(f.victim); err != nil {
		t.Fatal(err)
	}
	before := indexSnapshot(t, f.s)
	if err := f.s.SetIndexedImage(f.victim, "poster", "/fixture/new.webp"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing: %v", err)
	}
	if indexSnapshot(t, f.s) != before {
		t.Fatal("missing update resurrected movie or bumped version")
	}
}

func TestSetIndexedImageUsesActualLibraryIncludingZero(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprint(zero), func(t *testing.T) {
			f := newIndexedDeleteFixture(t)
			oldLibrary := f.lib
			actual := f.other
			if zero {
				if _, err := f.s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/fixture/zero')"); err != nil {
					t.Fatal(err)
				}
				actual = 0
			}
			if _, err := f.s.db.Exec("UPDATE movies SET library_id=? WHERE id=?", actual, f.victim); err != nil {
				t.Fatal(err)
			}
			f.lib = actual
			f.other = oldLibrary
			if err := f.s.BumpVersion(actual); err != nil {
				t.Fatal(err)
			}
			before := indexedDeleteVersions(t, f)
			if err := f.s.SetIndexedImage(f.victim, "poster", "/fixture/new.webp"); err != nil {
				t.Fatal(err)
			}
			assertIndexedDeleteVersions(t, f, before, true)
		})
	}
}
