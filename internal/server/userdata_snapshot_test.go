package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"emby-go/internal/store"
)

func TestUserSettingsResponseReadFailureRollsBack(t *testing.T) {
	for op := range 3 {
		for _, event := range []string{"INSERT", "UPDATE"} {
			t.Run(fmt.Sprintf("%d/%s", op, event), func(t *testing.T) {
				f := newAdminDeleteFixture(t)
				if event == "INSERT" {
					if _, err := f.db.Exec("DELETE FROM userdata WHERE movie_id=?", f.victim); err != nil {
						t.Fatal(err)
					}
				}
				before, version := adminDeleteSnapshot(t, f), f.a.db.Version("g:version")
				// SQLite accepts this TEXT in an INTEGER-affinity column. The write
				// succeeds; scanning its response into int64 must fail before COMMIT.
				if _, err := f.db.Exec("CREATE TRIGGER response_read_fault AFTER " + event + " ON userdata BEGIN UPDATE userdata SET position_ticks='private-response-canary' WHERE movie_id=NEW.movie_id; END"); err != nil {
					t.Fatal(err)
				}
				w := userSettingRequest(f.a.Handler(), settingRoutes(f.victim)[op], "fixture-admin-delete")
				if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"code\":\"INTERNAL\",\"error\":\"user data write failed\"}" || strings.Contains(w.Body.String(), "private-response-canary") {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if adminDeleteSnapshot(t, f) != before || f.a.db.Version("g:version") != version {
					t.Fatal("response read failure committed data or version")
				}
				if _, err := f.db.Exec("DROP TRIGGER response_read_fault"); err != nil {
					t.Fatal(err)
				}
				w = userSettingRequest(f.a.Handler(), settingRoutes(f.victim)[op], "fixture-admin-delete")
				if w.Code != http.StatusOK {
					t.Fatalf("retry status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestUserSettingsResponseShapeAndCapturedValue(t *testing.T) {
	f := newAdminDeleteFixture(t)
	if _, err := f.db.Exec("UPDATE movies SET runtime_seconds=120 WHERE id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE userdata SET position_ticks=600000000,played=1,last_played_at='2026-10-17T00:00:00Z' WHERE movie_id=?", f.victim); err != nil {
		t.Fatal(err)
	}
	for _, route := range settingRoutes(f.victim) {
		w := userSettingRequest(f.a.Handler(), route, "fixture-admin-delete")
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		want := map[string]any{"ItemId": fmt.Sprint(f.victim), "ServerId": f.a.serverID, "PlaybackPositionTicks": float64(600000000), "PlayCount": float64(3), "Played": true, "IsFavorite": true, "PlayedPercentage": float64(50), "LastPlayedDate": "2026-10-17T00:00:00Z"}
		// Likes 按库内当前评分回显：未评分不出现，赞/踩输出 true/false。
		var likes int
		if err := f.db.QueryRow("SELECT likes FROM userdata WHERE movie_id=?", f.victim).Scan(&likes); err != nil {
			t.Fatalf("读取评分失败: %v", err)
		}
		if likes != 0 {
			want["Likes"] = likes > 0
		}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("response=%v want=%v", body, want)
		}
	}
	data, err := f.a.db.SetIndexedFavoriteData(f.victim, true)
	if err != nil {
		t.Fatal(err)
	}
	// Formatting a committed response is pure: a later deletion and closed Store
	// must not turn the captured response into a 404, zero DTO or a DB read error.
	if err := f.a.db.DeleteIndexedMovie(f.victim); err != nil {
		t.Fatal(err)
	}
	f.a.Close()
	got := f.a.userDataJSON(f.victim, data)
	if got["IsFavorite"] != true || got["PlayedPercentage"] != float64(50) || got["PlayCount"] != int64(3) {
		t.Fatalf("captured response changed: %v", got)
	}
	if zero := f.a.userDataJSON(99, store.UserData{}); zero["PlayedPercentage"] != float64(0) || zero["ItemId"] != "99" {
		t.Fatalf("zero formatting=%v", zero)
	}
}
