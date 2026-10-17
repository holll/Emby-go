package server

import (
	"fmt"
	"strings"
	"testing"
)

func TestExplicitPlayedAtomicAPI(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		for _, stage := range []string{"global", "library", "userdata", "commit"} {
			t.Run(method+"/"+stage, func(t *testing.T) {
				f := newAdminDeleteFixture(t)
				h := f.a.Handler()
				// Ensure both operations actually change the stored flag.
				if err := f.a.db.SetPlayed(f.victim, method == "DELETE"); err != nil {
					t.Fatal(err)
				}
				beforeData, err := f.a.db.Data(f.victim)
				if err != nil {
					t.Fatal(err)
				}
				route := userSettingRoute{"played", method, fmt.Sprintf("/Users/1/PlayedItems/%d", f.victim)}
				before := adminDeleteSnapshot(t, f)
				version := f.a.db.Version("g:version")
				var oldLib int
				if err := f.db.QueryRow("SELECT value FROM kv WHERE key=?", fmt.Sprintf("lib:%d:version", f.lib)).Scan(&oldLib); err != nil {
					t.Fatal(err)
				}
				if w := userSettingRequest(h, route, ""); w.Code != 401 {
					t.Fatalf("unauthorized=%d", w.Code)
				}
				if adminDeleteSnapshot(t, f) != before {
					t.Fatal("unauthorized write")
				}
				if stage == "commit" {
					if _, err := f.db.Exec(`CREATE TABLE played_api_parent(id INTEGER PRIMARY KEY);
CREATE TABLE played_api_child(id INTEGER REFERENCES played_api_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_played_api AFTER UPDATE ON userdata BEGIN INSERT INTO played_api_child VALUES(999); END;`); err != nil {
						t.Fatal(err)
					}
				} else {
					clause := map[string]string{"global": "BEFORE UPDATE ON kv WHEN OLD.key='g:version'", "library": fmt.Sprintf("BEFORE UPDATE ON kv WHEN OLD.key='lib:%d:version'", f.lib), "userdata": "BEFORE UPDATE ON userdata"}[stage]
					if _, err := f.db.Exec("CREATE TRIGGER fail_played_api " + clause + " BEGIN SELECT RAISE(ABORT,'played-api-private'); END"); err != nil {
						t.Fatal(err)
					}
				}
				w := userSettingRequest(h, route, "fixture-admin-delete")
				if w.Code != 500 || w.Body.String() != "{\"error\":\"user data write failed\"}" || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if adminDeleteSnapshot(t, f) != before || f.a.db.Version("g:version") != version {
					t.Fatal("failed write changed snapshot")
				}
				if _, err := f.db.Exec("DROP TRIGGER fail_played_api"); err != nil {
					t.Fatal(err)
				}
				for i := 1; i <= 2; i++ {
					w = userSettingRequest(h, route, "fixture-admin-delete")
					if w.Code != 204 || w.Body.Len() != 0 {
						t.Fatalf("success=%d body=%s", w.Code, w.Body.String())
					}
					expected := beforeData
					expected.Played = method == "POST"
					got, err := f.a.db.Data(f.victim)
					if err != nil || got != expected {
						t.Fatalf("data=%+v expected=%+v err=%v", got, expected, err)
					}
					var g, lib, old int
					if _, err := fmt.Sscan(version, &old); err != nil {
						t.Fatal(err)
					}
					if err := f.db.QueryRow("SELECT value FROM kv WHERE key='g:version'").Scan(&g); err != nil {
						t.Fatal(err)
					}
					if err := f.db.QueryRow("SELECT value FROM kv WHERE key=?", fmt.Sprintf("lib:%d:version", f.lib)).Scan(&lib); err != nil {
						t.Fatal(err)
					}
					if g != old+i || lib != oldLib+i || f.a.db.Version("g:version") != fmt.Sprint(g) {
						t.Fatalf("versions=%d/%d", g, lib)
					}
				}
				missing := userSettingRoute{"missing", method, "/Users/1/PlayedItems/999999"}
				if w := userSettingRequest(h, missing, "fixture-admin-delete"); w.Code != 404 {
					t.Fatalf("missing=%d", w.Code)
				}
			})
		}
	}
}
