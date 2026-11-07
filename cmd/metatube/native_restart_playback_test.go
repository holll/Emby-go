//go:build linux

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

// State here is only the client's retained credentials/IDs and observations;
// the restarted executable must recover the actual session bindings from SQLite.
type nativeRestartPlayback struct {
	token    string
	movie    int64
	other    int64
	baseline map[string]string
}

func (p *nativeRestartPlayback) afterReady(t *testing.T, base, dbPath string, run int) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Token", p.token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s: status=%d want=%d read=%v body=%s", path, resp.StatusCode, want, err, raw)
		}
		var result map[string]any
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	report := func(movie int64, session, suffix string, position int64, want int) map[string]any {
		t.Helper()
		return call("/Sessions/Playing"+suffix, fmt.Sprintf(`{"ItemId":%q,"PlaySessionId":%q,"PositionTicks":%d}`, fmt.Sprint(movie), session, position), want)
	}
	if run == 0 {
		call("/api/auth/initialize", `{"Username":"admin","Pw":"password-1234","Code":"native-fixture"}`, http.StatusNoContent)
		auth := call("/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`, http.StatusOK)
		p.token, _ = auth["AccessToken"].(string)
		if p.token == "" {
			t.Fatal("authentication returned no token")
		}
		// The service already created and owns the live DB/WAL. Seed metadata
		// only, without media access or replacing its files/ownership.
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			lib, err := db.AddLibrary("Restart playback", filepath.Dir(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			for i, dest := range []*int64{&p.movie, &p.other} {
				*dest, err = db.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("playback-%d.strm", i)), SourceProtocol: "https", Status: "success", Title: fmt.Sprintf("Restart movie %d", i)}, 1, time.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
		}()
		report(p.movie, "restart-active", "", 0, http.StatusNoContent)
		report(p.movie, "restart-active", "/Progress", 5000000, http.StatusNoContent)
		report(p.movie, "restart-stopped", "", 0, http.StatusNoContent)
		report(p.movie, "restart-stopped", "/Progress", 10000000, http.StatusNoContent)
		report(p.movie, "restart-stopped", "/Stopped", 12000000, http.StatusNoContent)
		p.baseline = nativeRestartPlaybackSnapshot(t, dbPath)
		return
	}
	if got := nativeRestartPlaybackSnapshot(t, dbPath); !reflect.DeepEqual(got, p.baseline) {
		t.Fatalf("restart changed playback rows: before=%v after=%v", p.baseline, got)
	}
	for _, session := range []string{"restart-active", "restart-stopped"} {
		for _, suffix := range []string{"", "/Progress", "/Stopped"} {
			result := report(p.other, session, suffix, 99999999, http.StatusConflict)
			if result["code"] != "PLAYBACK_SESSION_MOVIE_CONFLICT" {
				t.Fatalf("%s%s: wrong conflict envelope: %v", session, suffix, result)
			}
			if got := nativeRestartPlaybackSnapshot(t, dbPath); !reflect.DeepEqual(got, p.baseline) {
				t.Fatalf("%s%s conflict wrote playback state: before=%v after=%v", session, suffix, p.baseline, got)
			}
		}
	}
	// A legal retained-token/same-film replay must still be accepted. Check
	// this after the zero-write assertions, rather than using only rejections.
	report(p.movie, "restart-stopped", "/Stopped", 12000000, http.StatusNoContent)
	got := nativeRestartPlaybackSnapshot(t, dbPath)
	for table, before := range p.baseline {
		// A successful event invalidates cache versions even for a duplicate
		// stop; the idempotency contract covers playback rows/count, not kv.
		if table != "kv" && got[table] != before {
			t.Fatalf("duplicate stop after restart changed %s: before=%s after=%s", table, before, got[table])
		}
	}
	t.Log("active/stopped bindings retained across actual restart; six conflicts preserved selected tables/cache versions, duplicate stop preserved playback rows")
}

func nativeRestartPlaybackSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Require nonempty, semantically meaningful state before comparing rows;
	// empty-table equality would not prove persistence or conflict protection.
	var sessions, stopped, events, count, position int64
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM playback_sessions), (SELECT count(*) FROM playback_sessions WHERE stopped=1), (SELECT count(*) FROM playback_events), (SELECT play_count FROM userdata), (SELECT position_ticks FROM userdata)`).Scan(&sessions, &stopped, &events, &count, &position); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || stopped != 1 || events != 5 || count != 1 || position != 12000000 {
		t.Fatalf("invalid playback baseline: sessions=%d stopped=%d events=%d count=%d position=%d", sessions, stopped, events, count, position)
	}
	result := make(map[string]string)
	for _, table := range []string{"playback_sessions", "playback_events", "userdata", "sqlite_sequence", "kv"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		var values [][]any
		for rows.Next() {
			row := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range row {
				pointers[i] = &row[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			values = append(values, row)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("snapshot %s: %v %v", table, err, closeErr)
		}
		raw, err := json.Marshal(struct {
			Columns []string
			Rows    [][]any
		}{columns, values})
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(raw)
	}
	return result
}
