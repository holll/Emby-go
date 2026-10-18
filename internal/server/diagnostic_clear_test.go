package server

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"modernc.org/sqlite"
)

const diagnosticClearToken = "diagnostic-clear-session"

func diagnosticClearFixture(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	a, db, _ := probeRetryFixture(t)
	if err := a.db.SaveAccessToken(diagnosticClearToken); err != nil {
		t.Fatal(err)
	}
	return a, db
}

func diagnosticClearRequest(a *App, token string) *httptest.ResponseRecorder {
	return diagnosticClearRequestTo(a.Handler(), token)
}

func diagnosticClearRequestTo(h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/probe", nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func diagnosticClearUnknown(a *App, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
	return w
}

type diagnosticClearState struct {
	rows  []string
	seen  map[string]struct{}
	epoch uint64
}

func diagnosticClearSnapshot(t *testing.T, a *App, db *sql.DB) diagnosticClearState {
	t.Helper()
	state := diagnosticClearState{rows: []string{}, seen: map[string]struct{}{}}
	rows, err := db.Query("SELECT id,method,path,query,body_preview,created_at FROM api_probe ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var method, path, query, body, created string
		if err := rows.Scan(&id, &method, &path, &query, &body, &created); err != nil {
			t.Fatal(err)
		}
		state.rows = append(state.rows, fmt.Sprintf("%d %q %q %q %q %q", id, method, path, query, body, created))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	a.probeMu.Lock()
	for key := range a.probeSeen {
		state.seen[key] = struct{}{}
	}
	state.epoch = a.probeEpoch
	a.probeMu.Unlock()
	return state
}

func diagnosticClearAssertHTTP(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d body=%s; want %d", w.Code, w.Body.String(), status)
	}
	if status == http.StatusNoContent && w.Body.Len() != 0 {
		t.Fatalf("204 response has body %q", w.Body.String())
	}
}

func TestDiagnosticClearAuthenticatedLifecycle(t *testing.T) {
	a, db := diagnosticClearFixture(t)
	diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, "/Users/canary-initial?token=canary"), http.StatusNotFound)
	diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, "/System/canary-initial"), http.StatusNotFound)
	assertProbeRetryState(t, a, db, 2, 2)
	before := diagnosticClearSnapshot(t, a, db)

	for _, token := range []string{"", "invalid-clear-session"} {
		diagnosticClearAssertHTTP(t, diagnosticClearRequest(a, token), http.StatusUnauthorized)
		if after := diagnosticClearSnapshot(t, a, db); !reflect.DeepEqual(after, before) {
			t.Fatalf("unauthorized clear changed state: before=%+v after=%+v", before, after)
		}
	}

	diagnosticClearAssertHTTP(t, diagnosticClearRequest(a, diagnosticClearToken), http.StatusNoContent)
	assertProbeRetryState(t, a, db, 0, 0)
	cleared := diagnosticClearSnapshot(t, a, db)
	if cleared.epoch != before.epoch+1 {
		t.Fatalf("epoch=%d; want %d", cleared.epoch, before.epoch+1)
	}
	// Both initially collected resource families can be observed again, but only once.
	for _, path := range []string{"/Users/canary-new", "/Users/canary-duplicate", "/System/canary-new", "/System/canary-duplicate"} {
		diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, path), http.StatusNotFound)
	}
	assertProbeRetryState(t, a, db, 2, 2)
	diagnosticClearAssertHTTP(t, diagnosticClearRequest(a, diagnosticClearToken), http.StatusNoContent)
	for i := 0; i < 2; i++ {
		before := diagnosticClearSnapshot(t, a, db)
		diagnosticClearAssertHTTP(t, diagnosticClearRequest(a, diagnosticClearToken), http.StatusNoContent)
		assertProbeRetryState(t, a, db, 0, 0)
		if after := diagnosticClearSnapshot(t, a, db); after.epoch != before.epoch+1 {
			t.Fatalf("empty clear epoch=%d; want %d", after.epoch, before.epoch+1)
		}
	}
}

func TestDiagnosticClearDeleteFailurePreservesGeneration(t *testing.T) {
	a, db := diagnosticClearFixture(t)
	for _, path := range []string{"/Users/canary-first", "/System/canary-second"} {
		diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, path), http.StatusNotFound)
	}
	assertProbeRetryState(t, a, db, 2, 2)
	// Fail on the second row: SQLite must roll back even a partial statement delete.
	if _, err := db.Exec(`CREATE TRIGGER fail_diagnostic_clear BEFORE DELETE ON api_probe
		WHEN OLD.path = '/system/[UNMATCHED]'
		BEGIN SELECT RAISE(ABORT, 'canary DELETE FROM api_probe internal SQL failure'); END;`); err != nil {
		t.Fatal(err)
	}
	before := diagnosticClearSnapshot(t, a, db)
	for i := 0; i < 2; i++ {
		w := diagnosticClearRequest(a, diagnosticClearToken)
		diagnosticClearAssertHTTP(t, w, http.StatusInternalServerError)
		if w.Body.String() != `{"code":"INTERNAL","error":"probe history clear failed"}` {
			t.Fatalf("unexpected public clear error: %q", w.Body.String())
		}
		for _, secret := range []string{"canary", "DELETE", "api_probe", "SQL", "sqlite", "trigger"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("clear response leaked %q: %s", secret, w.Body.String())
			}
		}
		if after := diagnosticClearSnapshot(t, a, db); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed clear changed state: before=%+v after=%+v", before, after)
		}
	}
	// Failed clear must not reopen deduplication slots.
	diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, "/Users/canary-duplicate"), http.StatusNotFound)
	if after := diagnosticClearSnapshot(t, a, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("duplicate after failed clear changed state: %+v", after)
	}
	if _, err := db.Exec("DROP TRIGGER fail_diagnostic_clear"); err != nil {
		t.Fatal(err)
	}
	diagnosticClearAssertHTTP(t, diagnosticClearRequest(a, diagnosticClearToken), http.StatusNoContent)
	assertProbeRetryState(t, a, db, 0, 0)
	if after := diagnosticClearSnapshot(t, a, db); after.epoch != before.epoch+1 {
		t.Fatalf("retry epoch=%d; want %d", after.epoch, before.epoch+1)
	}
	diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, "/Users/canary-retry"), http.StatusNotFound)
	assertProbeRetryState(t, a, db, 1, 1)
}

func TestDiagnosticClearWaitsForInFlightInsert(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	releaseInsert := func() { once.Do(func() { close(release) }) }
	name := fmt.Sprintf("diagnostic_clear_insert_gate_%d", cacheGateSequence.Add(1))
	// modernc copies the registered functions when each connection opens.
	// Register before the app fixture (including its authority database) is opened.
	if err := sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return int64(1), nil
		case <-time.After(10 * time.Second):
			return nil, fmt.Errorf("diagnostic clear insert gate timed out")
		}
	}); err != nil {
		t.Fatal(err)
	}
	a, db := diagnosticClearFixture(t)
	if _, err := db.Exec("CREATE TRIGGER diagnostic_clear_insert_gate BEFORE INSERT ON api_probe BEGIN SELECT " + name + "(); END;"); err != nil {
		t.Fatal(err)
	}
	insertDone := make(chan *httptest.ResponseRecorder, 1)
	clearDone := make(chan *httptest.ResponseRecorder, 1)
	insertJoined, clearJoined := make(chan struct{}), make(chan struct{})
	clearStarted := false
	defer func() {
		releaseInsert()
		workers := []<-chan struct{}{insertJoined}
		if clearStarted {
			workers = append(workers, clearJoined)
		}
		for _, done := range workers {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("diagnostic clear worker did not join after gate release")
			}
		}
	}()
	go func() {
		defer close(insertJoined)
		insertDone <- diagnosticClearUnknown(a, "/Users/canary-in-flight")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("INSERT did not enter SQLite gate")
	}
	// The SQL callback proves that the writer is already inside persistence.
	// Its reservation must be visible, and probeMu must not be held across SQL.
	a.probeMu.Lock()
	_, reserved := a.probeSeen["POST /users/[UNMATCHED]"]
	epoch := a.probeEpoch
	a.probeMu.Unlock()
	if !reserved {
		t.Fatal("in-flight INSERT has no reservation")
	}
	if a.probePersistMu.TryLock() {
		a.probePersistMu.Unlock()
		t.Fatal("INSERT did not hold persistence serialization lock")
	}
	clearAtHandler := make(chan struct{})
	// Other tests exercise the complete application router. Here insert a signal
	// between the real authentication and clear handlers, so the INSERT stays
	// suspended until DELETE has actually passed authentication, not merely until
	// its goroutine was scheduled. No mock authentication or persistence is used.
	clearRouter := gin.New()
	clearRouter.DELETE("/api/admin/probe", a.requireAuth, func(c *gin.Context) {
		close(clearAtHandler)
		a.adminClearProbes(c)
	})
	clearStarted = true
	go func() {
		defer close(clearJoined)
		clearDone <- diagnosticClearRequestTo(clearRouter, diagnosticClearToken)
	}()
	select {
	case <-clearAtHandler:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent DELETE did not pass authentication while INSERT was gated")
	}
	// No timing-based negative assertion: release an observed INSERT, then join
	// both real API requests and inspect committed state.
	releaseInsert()
	select {
	case w := <-insertDone:
		diagnosticClearAssertHTTP(t, w, http.StatusNotFound)
	case <-time.After(5 * time.Second):
		t.Fatal("INSERT request did not complete")
	}
	select {
	case w := <-clearDone:
		diagnosticClearAssertHTTP(t, w, http.StatusNoContent)
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent clear request did not complete")
	}
	assertProbeRetryState(t, a, db, 0, 0)
	if after := diagnosticClearSnapshot(t, a, db); after.epoch != epoch+1 {
		t.Fatalf("clear epoch=%d; want %d", after.epoch, epoch+1)
	}
	diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, "/Users/canary-after-clear"), http.StatusNotFound)
	assertProbeRetryState(t, a, db, 1, 1)
}

func TestDiagnosticClearRejectsQueuedOldGeneration(t *testing.T) {
	a, db := diagnosticClearFixture(t)
	const method, path = "POST", "/users/[UNMATCHED]"
	const key = method + " " + path
	// Reproduce NoRoute's real reservation protocol, pausing before persistence
	// rather than trying to control the scheduler between mutex acquisitions.
	a.probeMu.Lock()
	oldEpoch := a.probeEpoch
	a.probeSeen[key] = probeSeenEntry{seenAt: time.Now()}
	a.probeMu.Unlock()
	assertProbeRetryState(t, a, db, 0, 1)
	diagnosticClearAssertHTTP(t, diagnosticClearRequest(a, diagnosticClearToken), http.StatusNoContent)
	assertProbeRetryState(t, a, db, 0, 0)
	a.persistDiagnosticProbe(method, path, "old-generation-canary", oldEpoch)
	assertProbeRetryState(t, a, db, 0, 0)

	a.probeMu.Lock()
	newEpoch := a.probeEpoch
	a.probeSeen[key] = probeSeenEntry{seenAt: time.Now()}
	a.probeMu.Unlock()
	if newEpoch != oldEpoch+1 {
		t.Fatalf("new epoch=%d; want %d", newEpoch, oldEpoch+1)
	}
	before := diagnosticClearSnapshot(t, a, db)
	// A late old writer must neither INSERT nor delete the new same-key slot.
	a.persistDiagnosticProbe(method, path, "old-generation-canary", oldEpoch)
	if after := diagnosticClearSnapshot(t, a, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("stale writer changed new-generation state: before=%+v after=%+v", before, after)
	}
	a.persistDiagnosticProbe(method, path, "new-generation", newEpoch)
	assertProbeRetryState(t, a, db, 1, 1)
	var query string
	if err := db.QueryRow("SELECT query FROM api_probe").Scan(&query); err != nil {
		t.Fatal(err)
	}
	if query != "new-generation" {
		t.Fatalf("persisted query=%q", query)
	}
	before = diagnosticClearSnapshot(t, a, db)
	diagnosticClearAssertHTTP(t, diagnosticClearUnknown(a, "/Users/canary-new-duplicate"), http.StatusNotFound)
	a.persistDiagnosticProbe(method, path, "old-generation-canary", oldEpoch)
	if after := diagnosticClearSnapshot(t, a, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("duplicate/stale writer changed persisted new generation: before=%+v after=%+v", before, after)
	}
}
