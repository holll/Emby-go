package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

const indexedAvatarActor = "Victim actor"

func actorIndexFixture(t *testing.T) indexedDeleteFixture {
	t.Helper()
	f := newIndexedDeleteFixture(t)
	if err := f.s.ReplaceActors(f.survivor, []ActorRef{{Name: indexedAvatarActor}, {Name: "Other actor"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.IndexMovie(Movie{LibraryID: f.lib, SourcePath: "/fixture/delete/second.strm", Title: "second", Status: "pending"}, 1, time.Unix(2, 0), []ActorRef{{Name: indexedAvatarActor}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func actorIndexVersions(t *testing.T, f indexedDeleteFixture) map[string]string {
	t.Helper()
	out := make(map[string]string)
	rows, err := f.s.db.Query("SELECT key,value FROM kv WHERE key='g:version' OR key LIKE 'lib:%:version'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertActorIndexBumps(t *testing.T, f indexedDeleteFixture, before map[string]string, libs ...int64) {
	t.Helper()
	want := make(map[string]string)
	for k, v := range before {
		want[k] = v
	}
	keys := []string{"g:version"}
	for _, id := range libs {
		keys = append(keys, fmt.Sprintf("lib:%d:version", id))
	}
	for _, key := range keys {
		n := 0
		if v := want[key]; v != "" {
			var err error
			n, err = strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
		}
		want[key] = strconv.Itoa(n + 1)
	}
	got := actorIndexVersions(t, f)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("versions %v want %v", got, want)
	}
	if f.s.Version("g:version") != want["g:version"] {
		t.Fatalf("mirror %s want %s", f.s.Version("g:version"), want["g:version"])
	}
}

func TestIndexedActorAvatarMultiLibraryAndCompatibility(t *testing.T) {
	f := actorIndexFixture(t)
	if _, err := f.s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(0,'Zero','/zero')"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.IndexMovie(Movie{LibraryID: 0, SourcePath: "/zero/movie.strm", Status: "success"}, 1, time.Unix(3, 0), []ActorRef{{Name: indexedAvatarActor}}); err != nil {
		t.Fatal(err)
	}
	unrelated, err := f.s.AddLibrary("Unrelated", "/unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.BumpVersion(unrelated.ID); err != nil {
		t.Fatal(err)
	}
	movie, err := f.s.Movie(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	data, err := f.s.Data(f.victim)
	if err != nil {
		t.Fatal(err)
	}
	const stamp = "2001-02-03T04:05:06Z"
	// A historical sentinel detects accidental conflict timestamp updates even
	// when all calls happen in the same RFC3339 second; no timing sleeps.
	if _, err = f.s.db.Exec("UPDATE actors SET updated_at=? WHERE name=?", stamp, indexedAvatarActor); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ url, tag, wantURL, wantTag string }{{"remote-a", "tag-a", "remote-a", "tag-a"}, {"", "tag-b", "remote-a", "tag-b"}, {"remote-b", "", "remote-b", "tag-b"}, {"", "", "remote-b", "tag-b"}} {
		before := actorIndexVersions(t, f)
		if err = f.s.SetIndexedActorAvatar("  "+indexedAvatarActor+"  ", tc.url, tc.tag); err != nil {
			t.Fatal(err)
		}
		assertActorIndexBumps(t, f, before, f.lib, f.other, 0)
		actor, e := f.s.ActorAvatar(indexedAvatarActor)
		if e != nil || actor.AvatarURL != tc.wantURL || actor.AvatarTag != tc.wantTag {
			t.Fatalf("actor %+v %v", actor, e)
		}
		current, e := f.s.Movie(f.victim)
		if e != nil || fmt.Sprint(current) != fmt.Sprint(movie) {
			t.Fatalf("movie changed %v", e)
		}
		currentData, e := f.s.Data(f.victim)
		if e != nil || currentData != data {
			t.Fatalf("userdata changed %v", e)
		}
		var after string
		if e = f.s.db.QueryRow("SELECT updated_at FROM actors WHERE name=?", indexedAvatarActor).Scan(&after); e != nil || after != stamp {
			t.Fatalf("legacy timestamp changed %q %v", after, e)
		}
	}
	snapshot := indexSnapshot(t, f.s)
	if err = f.s.SetIndexedActorAvatar(" \t ", "ignored", "ignored"); err != nil || indexSnapshot(t, f.s) != snapshot {
		t.Fatal("blank name not a no-op", err)
	}
	before := actorIndexVersions(t, f)
	if err = f.s.SetIndexedActorAvatar("Unlinked actor", "unlinked-url", "unlinked-tag"); err != nil {
		t.Fatal(err)
	}
	assertActorIndexBumps(t, f, before)
	unlinked, err := f.s.ActorAvatar("Unlinked actor")
	if err != nil || unlinked.AvatarURL != "unlinked-url" || unlinked.AvatarTag != "unlinked-tag" {
		t.Fatalf("unlinked actor %+v %v", unlinked, err)
	}
	var insertedAt string
	if err = f.s.db.QueryRow("SELECT updated_at FROM actors WHERE name='Unlinked actor'").Scan(&insertedAt); err != nil {
		t.Fatal(err)
	}
	if _, err = time.Parse(time.RFC3339, insertedAt); err != nil || !strings.HasSuffix(insertedAt, "Z") {
		t.Fatalf("insert timestamp not UTC RFC3339: %q %v", insertedAt, err)
	}
	before = actorIndexVersions(t, f)
	if err = f.s.SetActorAvatar(indexedAvatarActor, "legacy-url", "legacy-tag"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(actorIndexVersions(t, f)) != fmt.Sprint(before) || f.s.Version("g:version") != before["g:version"] {
		t.Fatal("legacy setter changed version")
	}
}

func TestIndexedActorAvatarRollbackAndPoolHealth(t *testing.T) {
	for _, fault := range []string{"global", "first_library", "second_library", "actor_insert", "actor_update", "library_read", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f := actorIndexFixture(t)
			f.s.db.SetMaxOpenConns(1)
			var ddl string
			switch fault {
			case "global":
				ddl = "CREATE TRIGGER fail_actor_index BEFORE INSERT ON kv WHEN NEW.key='g:version' BEGIN SELECT RAISE(ABORT,'actor-index-canary'); END"
			case "first_library":
				ddl = fmt.Sprintf("CREATE TRIGGER fail_actor_index BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'actor-index-canary'); END", f.lib)
			case "second_library":
				ddl = fmt.Sprintf("CREATE TRIGGER fail_actor_index BEFORE INSERT ON kv WHEN NEW.key='lib:%d:version' BEGIN SELECT RAISE(ABORT,'actor-index-canary'); END", f.other)
			case "actor_insert":
				ddl = "CREATE TRIGGER fail_actor_index BEFORE INSERT ON actors BEGIN SELECT RAISE(ABORT,'actor-index-canary'); END"
			case "actor_update":
				ddl = "CREATE TRIGGER fail_actor_index BEFORE UPDATE ON actors BEGIN SELECT RAISE(ABORT,'actor-index-canary'); END"
			case "library_read":
				ddl = "ALTER TABLE movies RENAME TO actor_index_movies; CREATE VIEW movies AS SELECT id,'not-an-integer' AS library_id FROM actor_index_movies"
			case "commit":
				ddl = "CREATE TABLE actor_index_parent(id INTEGER PRIMARY KEY);CREATE TABLE actor_index_child(id INTEGER REFERENCES actor_index_parent(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER fail_actor_index AFTER UPDATE ON actors BEGIN INSERT INTO actor_index_child VALUES(999); END"
			}
			if _, err := f.s.db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			before := indexSnapshot(t, f.s)
			if err := f.s.SetIndexedActorAvatar(indexedAvatarActor, "new-url", "new-tag"); err == nil {
				t.Fatal("expected write failure")
			}
			if got := indexSnapshot(t, f.s); got != before {
				t.Fatal("failed actor write changed snapshot")
			}
			if fault == "commit" && indexedDeleteCount(t, f.s, "SELECT count(*) FROM actor_index_child") != 0 {
				t.Fatal("deferred child not rolled back")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("pool unhealthy: %v", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			cleanup := "DROP TRIGGER fail_actor_index"
			if fault == "library_read" {
				cleanup = "DROP VIEW movies;ALTER TABLE actor_index_movies RENAME TO movies"
			}
			if _, err = f.s.db.Exec(cleanup); err != nil {
				t.Fatal(err)
			}
			versions := actorIndexVersions(t, f)
			if err = f.s.SetIndexedActorAvatar(indexedAvatarActor, "new-url", "new-tag"); err != nil {
				t.Fatal(err)
			}
			assertActorIndexBumps(t, f, versions, f.lib, f.other)
		})
	}
}
