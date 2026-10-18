package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminDetailStorageErrorsAreNotMissingOrEmpty(t *testing.T) {
	faults := []struct{ name, damage, repair string }{
		{"movie_scan", "UPDATE movies SET runtime_seconds='private-detail-canary'", "UPDATE movies SET runtime_seconds=0"},
		{"movie_query", "ALTER TABLE movies RENAME COLUMN runtime_seconds TO private_detail_column", "ALTER TABLE movies RENAME COLUMN private_detail_column TO runtime_seconds"},
		{"userdata_scan", "UPDATE userdata SET position_ticks='private-detail-canary'", "UPDATE userdata SET position_ticks=120"},
		{"actors_scan", "ALTER TABLE movie_actors RENAME TO fixture_detail_actors; CREATE VIEW movie_actors AS SELECT movie_id,NULL AS actor_name FROM fixture_detail_actors", "DROP VIEW movie_actors; ALTER TABLE fixture_detail_actors RENAME TO movie_actors"},
		{"actors_query", "ALTER TABLE movie_actors RENAME COLUMN actor_name TO private_detail_column", "ALTER TABLE movie_actors RENAME COLUMN private_detail_column TO actor_name"},
		{"library_scan", "ALTER TABLE libraries RENAME TO fixture_detail_libraries; CREATE VIEW libraries AS SELECT id,NULL AS name,path,enabled FROM fixture_detail_libraries", "DROP VIEW libraries; ALTER TABLE fixture_detail_libraries RENAME TO libraries"},
		{"library_query", "ALTER TABLE libraries RENAME COLUMN enabled TO private_detail_column", "ALTER TABLE libraries RENAME COLUMN private_detail_column TO enabled"},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			h := f.a.Handler()
			path := fmt.Sprintf("/api/admin/items/%d/detail", f.victim)
			baseline := associationGET(h, path, "fixture-admin-delete")
			if baseline.Code != 200 {
				t.Fatalf("baseline %d %s", baseline.Code, baseline.Body.String())
			}
			before := f.a.db.Version("g:version")
			if _, err := f.db.Exec(fault.damage); err != nil {
				t.Fatal(err)
			}
			if w := associationGET(h, path, ""); w.Code != 401 {
				t.Fatalf("unauthorized %d", w.Code)
			}
			w := associationGET(h, path, "fixture-admin-delete")
			if w.Code != 500 || w.Body.String() != `{"code":"ITEM_DATA_UNAVAILABLE","error":"item data unavailable"}` {
				t.Fatalf("%s got %d %s", fault.name, w.Code, w.Body.String())
			}
			if f.a.db.Version("g:version") != before {
				t.Fatal("failed read changed version")
			}
			if _, err := f.db.Exec(fault.repair); err != nil {
				t.Fatal(err)
			}
			w = associationGET(h, path, "fixture-admin-delete")
			if w.Code != 200 || w.Body.String() != baseline.Body.String() {
				t.Fatalf("recovery %d %s; want %s", w.Code, w.Body.String(), baseline.Body.String())
			}
		})
	}
}

func TestAdminDetailMissingAndDisabledDataRemainCompatible(t *testing.T) {
	f := newAdminDeleteFixture(t)
	h := f.a.Handler()
	for _, id := range []string{"not-an-id", "9223372036854775807"} {
		w := associationGET(h, "/api/admin/items/"+id+"/detail", "fixture-admin-delete")
		if w.Code != 404 || w.Body.String() != `{"code":"NOT_FOUND","error":"not found"}` {
			t.Fatalf("missing %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	if _, err := f.db.Exec("UPDATE libraries SET enabled=0; DELETE FROM userdata; DELETE FROM movie_actors"); err != nil {
		t.Fatal(err)
	}
	// Files and NFO metadata are deliberately best-effort, unlike SQL errors.
	if _, err := f.db.Exec("UPDATE movies SET source_path=source_path||'.missing',nfo_path=nfo_path||'.missing'"); err != nil {
		t.Fatal(err)
	}
	w := associationGET(h, fmt.Sprintf("/api/admin/items/%d/detail", f.victim), "fixture-admin-delete")
	if w.Code != 200 {
		t.Fatalf("legacy missing %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Library string        `json:"library_name"`
		HasData bool          `json:"has_userdata"`
		Actors  []detailActor `json:"actors"`
		Files   []detailFile  `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Library != "" || body.HasData || body.Actors == nil || len(body.Actors) != 0 || len(body.Files) != 1 || body.Files[0].Size != 0 || body.Files[0].Probed {
		t.Fatalf("legacy fallbacks %+v", body)
	}
}

func TestAdminDetailClosedStoreIsUnavailableAfterRouting(t *testing.T) {
	f := newAdminDeleteFixture(t)
	f.a.Close()
	// Auth on the actual Handler returns AUTH_UNAVAILABLE first. Exercise the
	// detail handler itself to verify its independent Movie query boundary.
	r := gin.New()
	r.GET("/detail/:id", f.a.adminItemDetail)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/detail/%d", f.victim), nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), "ITEM_DATA_UNAVAILABLE") || strings.Contains(w.Body.String(), "closed") {
		t.Fatalf("closed %d %s", w.Code, w.Body.String())
	}
}
