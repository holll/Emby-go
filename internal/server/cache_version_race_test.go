package server

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
	"modernc.org/sqlite"
)

var cacheGateSequence atomic.Uint64

type cacheReadGate struct {
	entered chan string
	release chan struct{}
	first   sync.Once
	unblock sync.Once
}

func (g *cacheReadGate) releaseRead() { g.unblock.Do(func() { close(g.release) }) }

// The temporary view blocks inside a real SQLite SELECT, after SQLite has read
// the old title. A separate WAL writer can commit while that snapshot is held.
// No production hook, artificial sleep, or alteration of the migrated schema
// outside this disposable fixture is required.
func newCacheReadFixture(t *testing.T, prepare func(adminDeleteFixture)) (adminDeleteFixture, *cacheReadGate) {
	t.Helper()
	g := &cacheReadGate{entered: make(chan string, 1), release: make(chan struct{})}
	name := fmt.Sprintf("cache_read_gate_%d", cacheGateSequence.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var err error
		g.first.Do(func() {
			g.entered <- fmt.Sprint(args[0])
			select {
			case <-g.release:
			case <-time.After(10 * time.Second):
				err = fmt.Errorf("cache read gate not released")
			}
		})
		return args[0], err
	}); err != nil {
		t.Fatal(err)
	}
	// The function must be registered before Store opens its connections.
	f := newAdminDeleteFixture(t)
	if prepare != nil {
		prepare(f)
	}
	t.Cleanup(g.releaseRead)
	var mode string
	if err := f.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode=%q err=%v", mode, err)
	}
	rows, err := f.db.Query("SELECT name FROM pragma_table_info('movies') ORDER BY cid")
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		quoted := `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
		if column == "title" {
			quoted = name + "(" + quoted + ") AS " + quoted
		}
		columns = append(columns, quoted)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("ALTER TABLE movies RENAME TO cache_fixture_movies"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("CREATE VIEW movies AS SELECT " + strings.Join(columns, ",") + " FROM cache_fixture_movies"); err != nil {
		t.Fatal(err)
	}
	return f, g
}

func waitCacheRead(t *testing.T, g *cacheReadGate) string {
	t.Helper()
	select {
	case title := <-g.entered:
		return title
	case <-time.After(10 * time.Second):
		t.Fatal("movie SELECT did not enter read gate")
		return ""
	}
}

func joinCacheRead(t *testing.T, g *cacheReadGate, done <-chan struct{}) {
	t.Helper()
	g.releaseRead()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("movie reader did not finish after release")
	}
}

func TestCacheVersionRaceMetadataLateFill(t *testing.T) {
	f, g := newCacheReadFixture(t, nil)
	beforeVersion := f.a.db.Version("g:version")
	var old store.Movie
	var oldOK bool
	done := make(chan struct{})
	t.Cleanup(func() { joinCacheRead(t, g, done) })
	go func() { defer close(done); old, oldOK = f.a.cachedMovie(f.victim) }()
	oldTitle := waitCacheRead(t, g)
	if _, err := f.db.Exec("UPDATE cache_fixture_movies SET title='Committed new title',poster_path='/new.webp' WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.BumpVersion(f.lib); err != nil {
		t.Fatal(err)
	}
	afterVersion := f.a.db.Version("g:version")
	if afterVersion == beforeVersion {
		t.Fatal("writer did not publish")
	}
	joinCacheRead(t, g, done)
	if !oldOK || old.Title != oldTitle {
		t.Fatalf("reader did not retain old SQLite snapshot: %+v ok=%v old=%q", old, oldOK, oldTitle)
	}
	if _, ok := f.a.imgMeta.Get(movieMetadataKey(afterVersion, f.victim)); ok {
		t.Fatal("late old metadata filled current version")
	}
	current, ok := f.a.cachedMovie(f.victim)
	if !ok || current.Title != "Committed new title" || current.PosterPath != "/new.webp" {
		t.Fatalf("late old fill poisoned new request: %+v ok=%v", current, ok)
	}
	if _, ok := f.a.imgMeta.Get(movieMetadataKey(beforeVersion, f.victim)); !ok {
		t.Fatal("old read did not fill old namespace")
	}
	// Raw backing-table changes only create the race; publish explicitly. This
	// does not claim the fixture uses IndexMovie's separate atomic-write contract.
	if _, err := f.db.Exec("DELETE FROM cache_fixture_movies WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.BumpVersion(f.lib); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.a.cachedMovie(f.victim); ok {
		t.Fatal("deleted movie revived by late metadata")
	}
}

func TestCacheVersionRaceSimilarSourceSnapshot(t *testing.T) {
	var newerID int64
	f, g := newCacheReadFixture(t, func(f adminDeleteFixture) {
		for _, item := range []struct {
			id   int64
			year int
		}{{f.victim, 2000}, {f.survivor, 2001}} {
			m, err := f.a.db.Movie(item.id)
			if err != nil {
				t.Fatal(err)
			}
			m.Year = item.year
			if _, err := f.a.db.IndexMovie(m, 1, time.Unix(1, 0), []store.ActorRef{{Name: "Actor"}}); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		newerID, err = f.a.db.IndexMovie(store.Movie{LibraryID: f.other, SourcePath: "/fixture/newer.strm", NFOPath: "/fixture/newer.nfo", Title: "Newer candidate", Status: "success", Year: 2021, Genres: []string{"Drama"}}, 1, time.Unix(1, 0), []store.ActorRef{{Name: "Actor"}})
		if err != nil {
			t.Fatal(err)
		}
	})
	r := gin.New()
	r.GET("/similar/:id", f.a.similar)
	url := "/similar/" + strconv.FormatInt(f.victim, 10) + "?Limit=1"
	beforeVersion := f.a.db.Version("g:version")
	key := func(version string) string { return fmt.Sprintf("similar:%s:1:all:%d:1", version, f.victim) }
	oldResponse := httptest.NewRecorder()
	done := make(chan struct{})
	t.Cleanup(func() { joinCacheRead(t, g, done) })
	go func() { defer close(done); r.ServeHTTP(oldResponse, httptest.NewRequest("GET", url, nil)) }()
	waitCacheRead(t, g)
	if _, err := f.db.Exec("UPDATE cache_fixture_movies SET year=2020 WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.BumpVersion(f.lib); err != nil {
		t.Fatal(err)
	}
	afterVersion := f.a.db.Version("g:version")
	if afterVersion == beforeVersion {
		t.Fatal("writer did not publish")
	}
	joinCacheRead(t, g, done)
	assertTop := func(w *httptest.ResponseRecorder, want int64) {
		t.Helper()
		var body struct {
			Items []struct {
				ID string `json:"Id"`
			}
			TotalRecordCount int
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Items) != 1 || body.Items[0].ID != strconv.FormatInt(want, 10) || body.TotalRecordCount != 1 {
			t.Fatalf("want top=%d status=%d body=%s", want, w.Code, w.Body.String())
		}
	}
	assertTop(oldResponse, f.survivor)
	if _, ok := f.a.cache.Get(key(afterVersion)); ok {
		t.Fatal("old source snapshot filled new similar namespace")
	}
	if _, ok := f.a.cache.Get(key(beforeVersion)); !ok {
		t.Fatal("old source result did not fill old namespace")
	}
	current := httptest.NewRecorder()
	r.ServeHTTP(current, httptest.NewRequest("GET", url, nil))
	assertTop(current, newerID)
	if _, ok := f.a.cache.Get(key(afterVersion)); !ok {
		t.Fatal("new source result did not fill new namespace")
	}
}
