package server

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"emby-go/internal/store"
)

func movieMetadataKey(version string, id int64) string {
	return fmt.Sprintf("m:%s:%d", version, id)
}

func TestCachedMovieVersionRefreshAndDeletion(t *testing.T) {
	f := newAdminDeleteFixture(t)
	oldVersion := f.a.db.Version("g:version")
	old, ok := f.a.cachedMovie(f.victim)
	if !ok {
		t.Fatal("initial movie missing")
	}
	oldKey := movieMetadataKey(oldVersion, f.victim)
	if _, ok := f.a.imgMeta.Get(oldKey); !ok {
		t.Fatal("movie not cached in captured version")
	}
	updated := old
	updated.Title = "New title"
	updated.PosterPath = "/fixture/new-poster.webp"
	id, err := f.a.db.IndexMovie(updated, 2, time.Unix(2, 0), []store.ActorRef{{Name: "Actor"}})
	if err != nil || id != f.victim {
		t.Fatalf("update id=%d err=%v", id, err)
	}
	newVersion := f.a.db.Version("g:version")
	if oldVersion == newVersion {
		t.Fatal("update did not publish")
	}
	got, ok := f.a.cachedMovie(f.victim)
	if !ok || got.Title != updated.Title || got.PosterPath != updated.PosterPath {
		t.Fatalf("stale cached metadata: %+v ok=%v", got, ok)
	}
	if _, ok := f.a.imgMeta.Get(oldKey); !ok {
		t.Fatal("test must retain old namespace rather than rely on clear")
	}
	newKey := movieMetadataKey(newVersion, f.victim)
	if _, ok := f.a.imgMeta.Get(newKey); !ok {
		t.Fatal("new namespace not filled")
	}
	if err := f.a.db.DeleteIndexedMovie(f.victim); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.a.cachedMovie(f.victim); ok {
		t.Fatal("deleted movie revived by old cached metadata")
	}
	if _, ok := f.a.imgMeta.Get(newKey); !ok {
		t.Fatal("delete regression must retain old cached row")
	}
	if _, ok := f.a.imgMeta.Get(movieMetadataKey(f.a.db.Version("g:version"), f.victim)); ok {
		t.Fatal("missing row was incorrectly cached")
	}
}

func TestCachedMovieVersionIgnoresLegacyAndRepairsMalformedJSON(t *testing.T) {
	f := newAdminDeleteFixture(t)
	poison, _ := json.Marshal(store.Movie{ID: f.victim, Title: "Legacy poison"})
	f.a.imgMeta.Set(fmt.Sprintf("m:%d", f.victim), poison, time.Hour)
	got, ok := f.a.cachedMovie(f.victim)
	if !ok || got.Title == "Legacy poison" {
		t.Fatalf("legacy namespace used: %+v ok=%v", got, ok)
	}
	key := movieMetadataKey(f.a.db.Version("g:version"), f.victim)
	f.a.imgMeta.Set(key, []byte("{malformed"), time.Hour)
	got, ok = f.a.cachedMovie(f.victim)
	if !ok || got.Title == "" || got.Title == "Legacy poison" {
		t.Fatalf("malformed cache not recovered: %+v ok=%v", got, ok)
	}
	raw, exists := f.a.imgMeta.Get(key)
	var repaired store.Movie
	if !exists || json.Unmarshal(raw, &repaired) != nil || repaired.ID != f.victim || repaired.Title != got.Title {
		t.Fatalf("cache repair=%s exists=%v", raw, exists)
	}
}

func TestCachedMovieVersionStillCachesWithinGeneration(t *testing.T) {
	f := newAdminDeleteFixture(t)
	before, ok := f.a.cachedMovie(f.victim)
	if !ok {
		t.Fatal("initial movie missing")
	}
	version := f.a.db.Version("g:version")
	// Deliberately unversioned fixture write: this verifies a real cache hit,
	// not a disguised DB read on every image. Legacy writers still need migration.
	if _, err := f.db.Exec("UPDATE movies SET title='Unpublished fixture title' WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	got, ok := f.a.cachedMovie(f.victim)
	if !ok || got.Title != before.Title || f.a.db.Version("g:version") != version {
		t.Fatalf("same-generation cache miss: %+v ok=%v", got, ok)
	}
	if err := f.a.db.BumpVersion(f.lib); err != nil {
		t.Fatal(err)
	}
	got, ok = f.a.cachedMovie(f.victim)
	if !ok || got.Title != "Unpublished fixture title" {
		t.Fatalf("published update not visible: %+v ok=%v", got, ok)
	}
}
