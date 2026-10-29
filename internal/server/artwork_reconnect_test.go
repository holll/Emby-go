package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/store"
)

func TestArtworkLatestStatusReconnectAndTargetSnapshot(t *testing.T) {
	f := artworkFixture(t)
	latest := func() artworkStatus {
		t.Helper()
		w := translationCall(f.a, f.token, "GET", "/api/admin/artwork/status", "")
		if w.Code != 200 {
			t.Fatalf("latest: %d %s", w.Code, w.Body.String())
		}
		var status artworkStatus
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if s := latest(); s.TaskID != 0 || s.Running {
		t.Fatalf("unexpected initial status: %+v", s)
	}
	entered := make(chan struct{})
	f.a.artwork.run = func(ctx context.Context, _ *store.Store, _ artwork.Request, _ artwork.Options) (artwork.Result, error) {
		close(entered)
		<-ctx.Done()
		return artwork.Result{}, ctx.Err()
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie))
	if w.Code != 202 {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	<-entered
	s := latest()
	if s.TaskID <= 0 || !s.Running || len(s.MovieIDs) != 1 || s.MovieIDs[0] != f.movie {
		t.Fatalf("reconnect status: %+v", s)
	}
	for _, query := range []string{"?task_id=", "?task_id=0", "?task_id=-1", "?task_id=not-an-id"} {
		if w := translationCall(f.a, f.token, "GET", "/api/admin/artwork/status"+query, ""); w.Code != 400 {
			t.Fatalf("%s: %d", query, w.Code)
		}
	}
	if s.InstanceID == "" || s.InstanceID != f.a.instanceID {
		t.Fatalf("missing process identity: %+v", s)
	}
	for _, query := range []string{fmt.Sprintf("?task_id=%d", s.TaskID), fmt.Sprintf("?task_id=%d&instance_id=%s&instance_id=stale", s.TaskID, s.InstanceID)} {
		if got := translationCall(f.a, f.token, "GET", "/api/admin/artwork/status"+query, ""); got.Code != 400 {
			t.Fatalf("ambiguous identity: %d %s", got.Code, got.Body.String())
		}
	}
	staleQuery := fmt.Sprintf("?task_id=%d&instance_id=previous-process", s.TaskID)
	if got := translationCall(f.a, f.token, "GET", "/api/admin/artwork/status"+staleQuery, ""); got.Code != 404 {
		t.Fatalf("stale instance lookup: %d %s", got.Code, got.Body.String())
	}
	staleCancel := fmt.Sprintf(`{"task_id":%d,"instance_id":"previous-process"}`, s.TaskID)
	if got := translationCall(f.a, f.token, "POST", "/api/admin/artwork/cancel", staleCancel); got.Code != 404 {
		t.Fatalf("stale instance cancel: %d %s", got.Code, got.Body.String())
	}
	if !latest().Running {
		t.Fatal("stale instance cancelled current task")
	}
	w = translationCall(f.a, f.token, "POST", "/api/admin/artwork/cancel", fmt.Sprintf(`{"task_id":%d,"instance_id":%q}`, s.TaskID, s.InstanceID))
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	artworkJoin(t, f.a)
	if s = latest(); s.Running || !s.Cancelled || len(s.MovieIDs) != 1 || s.MovieIDs[0] != f.movie {
		t.Fatalf("terminal reconnect: %+v", s)
	}
}

func TestArtworkStrictJSONBoundaries(t *testing.T) {
	f := artworkFixture(t)
	for _, path := range []string{"run", "cancel"} {
		bodies := []string{
			`{"Task_ID":1}`,
			`{"task_id":1,"Task_ID":2}`,
			`{"mode":"provider","Mode":"screenshot","movie_ids":[1]}`,
			`{"mode":"screenshot","movie_ids":[1],"position_seconds":1e20}`,
			`{"task_id":1,"task_id":2}`,
			`{"mode":"provider","mode":"screenshot","movie_ids":[1]}`,
			`{"task_id":null}`,
			`{"task_id":1} {}`,
			`{"task_id":1,"padding":"` + strings.Repeat("x", 65536) + `"}`,
			strings.Repeat(" ", 65537) + `{}`,
		}
		for _, body := range bodies {
			w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/"+path, body)
			if w.Code != 400 {
				t.Fatalf("%s body length %d: %d %s", path, len(body), w.Code, w.Body.String())
			}
		}
	}
	if f.a.artwork.status.TaskID != 0 {
		t.Fatal("invalid input admitted work")
	}
}

func TestArtworkCacheRefreshKeepsUnrelatedEntries(t *testing.T) {
	f := artworkFixture(t)
	before, ok := f.a.cachedMovie(f.movie)
	if !ok {
		t.Fatal("movie missing")
	}
	oldVersion := f.a.db.Version("g:version")
	oldKey := movieMetadataKey(oldVersion, f.movie)
	f.a.cache.Set("unrelated:artwork-sentinel", []byte("keep"), time.Hour)
	f.a.tags[before.PosterPath] = tagEntry{tag: "old"}
	f.a.tags["new-poster"] = tagEntry{tag: "old"}
	f.a.artwork.run = func(ctx context.Context, db *store.Store, req artwork.Request, _ artwork.Options) (artwork.Result, error) {
		m, err := db.Movie(req.MovieID)
		if err != nil {
			return artwork.Result{}, err
		}
		lib, err := db.Library(m.LibraryID)
		if err != nil {
			return artwork.Result{}, err
		}
		err = db.CompareAndSwapArtwork(ctx, m, lib.Path, "new-poster", nil)
		return artwork.Result{Changed: err == nil, PosterPath: "new-poster"}, err
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie))
	if w.Code != 202 {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	if s := artworkJoin(t, f.a); s.Success != 1 {
		t.Fatalf("status: %+v", s)
	}
	if value, ok := f.a.cache.Get("unrelated:artwork-sentinel"); !ok || string(value) != "keep" {
		t.Fatal("unrelated cache was flushed")
	}
	if _, ok := f.a.imgMeta.Get(oldKey); !ok {
		t.Fatal("old namespace should not require cache flush")
	}
	if got, ok := f.a.cachedMovie(f.movie); !ok || got.PosterPath != "new-poster" || got.Title != before.Title {
		t.Fatalf("stale artwork metadata: %+v", got)
	}
	if f.a.db.Version("g:version") == oldVersion {
		t.Fatal("version not advanced")
	}
	for _, p := range []string{before.PosterPath, "new-poster"} {
		if _, ok := f.a.tags[p]; ok {
			t.Fatalf("stale image tag: %s", p)
		}
	}
}
