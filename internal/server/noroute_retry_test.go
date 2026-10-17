package server

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"modernc.org/sqlite"
)

func probeRetryFixture(t *testing.T) (*App, *sql.DB, *gin.Engine) {
	t.Helper()
	a, _, path := authorityApp(t)
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := gin.New()
	r.NoRoute(a.noRoute)
	return a, db, r
}

func requestUnknown(t *testing.T, r *gin.Engine, path string) {
	t.Helper()
	body := &diagnosticUnreadBody{}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", path, body))
	if w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" || body.reads != 0 || strings.Contains(w.Body.String(), "canary") {
		t.Errorf("status=%d cache=%q reads=%d body=%s", w.Code, w.Header().Get("Cache-Control"), body.reads, w.Body.String())
	}
}

func assertProbeRetryState(t *testing.T, a *App, db *sql.DB, wantRows, wantSeen int) {
	t.Helper()
	var rows int
	if err := db.QueryRow("SELECT count(*) FROM api_probe").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	a.probeMu.Lock()
	seen := len(a.probeSeen)
	a.probeMu.Unlock()
	if rows != wantRows || seen != wantSeen {
		t.Fatalf("rows=%d seen=%d; want %d/%d", rows, seen, wantRows, wantSeen)
	}
}

func TestNoRouteRetriesFailedPersistence(t *testing.T) {
	for _, phase := range []string{"insert", "commit"} {
		t.Run(phase, func(t *testing.T) {
			a, db, r := probeRetryFixture(t)
			ddl := `CREATE TRIGGER fail_probe BEFORE INSERT ON api_probe BEGIN SELECT RAISE(ABORT,'canary insert failure'); END;`
			if phase == "commit" {
				ddl = `CREATE TABLE probe_parent(id INTEGER PRIMARY KEY);
     CREATE TABLE probe_child(parent_id INTEGER REFERENCES probe_parent(id) DEFERRABLE INITIALLY DEFERRED);
     CREATE TRIGGER fail_probe AFTER INSERT ON api_probe BEGIN INSERT INTO probe_child VALUES(1); END;`
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				requestUnknown(t, r, fmt.Sprintf("/Users/canary-%d?token=canary-query", i))
				assertProbeRetryState(t, a, db, 0, 0)
			}
			if phase == "commit" {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM probe_child").Scan(&count); err != nil || count != 0 {
					t.Fatalf("child=%d err=%v", count, err)
				}
			}
			if _, err := db.Exec("DROP TRIGGER fail_probe"); err != nil {
				t.Fatal(err)
			}
			requestUnknown(t, r, "/Users/canary-retry?token=canary-query")
			requestUnknown(t, r, "/Users/canary-duplicate?token=canary-query")
			assertProbeRetryState(t, a, db, 1, 1)
			var method, path, query, body string
			if err := db.QueryRow("SELECT method,path,query,body_preview FROM api_probe").Scan(&method, &path, &query, &body); err != nil {
				t.Fatal(err)
			}
			if method != "POST" || path != "/users/[UNMATCHED]" || body != "[OMITTED]" || strings.Contains(query, "canary") {
				t.Fatalf("unsafe row %q %q %q %q", method, path, query, body)
			}
		})
	}
}

func TestNoRouteInFlightReservationDeduplicatesAndReleases(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var unblock sync.Once
	releaseWrite := func() { unblock.Do(func() { close(release) }) }
	name := fmt.Sprintf("probe_write_gate_%d", cacheGateSequence.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return int64(1), nil
		case <-time.After(10 * time.Second):
			return nil, fmt.Errorf("probe write gate not released")
		}
	}); err != nil {
		t.Fatal(err)
	}
	a, db, r := probeRetryFixture(t)
	t.Cleanup(releaseWrite)
	if _, err := db.Exec("CREATE TRIGGER fail_probe BEFORE INSERT ON api_probe BEGIN SELECT " + name + "(); SELECT RAISE(ABORT,'canary failure'); END;"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); requestUnknown(t, r, "/Users/canary-first") }()
	defer func() {
		releaseWrite()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Error("first request did not finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first write did not enter gate")
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); requestUnknown(t, r, fmt.Sprintf("/Users/canary-%d", i)) }(i)
	}
	duplicates := make(chan struct{})
	go func() { wg.Wait(); close(duplicates) }()
	select {
	case <-duplicates:
	case <-time.After(3 * time.Second):
		releaseWrite()
		<-duplicates
		t.Fatal("duplicates waited for database write")
	}
	assertProbeRetryState(t, a, db, 0, 1)
	releaseWrite()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed write did not finish")
	}
	assertProbeRetryState(t, a, db, 0, 0)
	if _, err := db.Exec("DROP TRIGGER fail_probe"); err != nil {
		t.Fatal(err)
	}
	requestUnknown(t, r, "/Users/canary-retry")
	assertProbeRetryState(t, a, db, 1, 1)
}

func TestNoRouteFailedWriteDoesNotConsumeBoundedSlot(t *testing.T) {
	a, db, r := probeRetryFixture(t)
	a.probeMu.Lock()
	for i := 0; i < 127; i++ {
		a.probeSeen[fmt.Sprint(i)] = struct{}{}
	}
	a.probeMu.Unlock()
	if _, err := db.Exec("CREATE TRIGGER fail_probe BEFORE INSERT ON api_probe BEGIN SELECT RAISE(ABORT,'canary failure'); END;"); err != nil {
		t.Fatal(err)
	}
	requestUnknown(t, r, "/Users/canary-failed")
	assertProbeRetryState(t, a, db, 0, 127)
	if _, err := db.Exec("DROP TRIGGER fail_probe"); err != nil {
		t.Fatal(err)
	}
	requestUnknown(t, r, "/Users/canary-retry")
	assertProbeRetryState(t, a, db, 1, 128)
	requestUnknown(t, r, "/System/canary-over-capacity")
	assertProbeRetryState(t, a, db, 1, 128)
}
