package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSpritesAdmissionAfterBodyParsing(t *testing.T) {
	for _, path := range []string{"run", "cancel"} {
		t.Run(path, func(t *testing.T) {
			f := artworkFixture(t)
			raw := fmt.Sprintf(`{"movie_ids":[%d]}`, f.movie)
			if path == "cancel" {
				raw = fmt.Sprintf(`{"task_id":1,"instance_id":%q}`, f.a.instanceID)
			}
			read := false
			body := &passwordIdentityReader{Reader: strings.NewReader(raw), beforeRead: func() {
				read = true
				if err := f.a.db.DeleteAccessToken(f.token); err != nil {
					t.Fatal(err)
				}
			}}
			req := httptest.NewRequest("POST", "/api/admin/sprites/"+path, body)
			req.ContentLength = int64(len(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Emby-Token", f.token)
			w := httptest.NewRecorder()
			f.a.Handler().ServeHTTP(w, req)
			if !read || w.Code != 403 {
				t.Fatalf("read=%v code=%d body=%s", read, w.Code, w.Body.String())
			}
			if f.a.sprites.status.TaskID != 0 {
				t.Fatal("revoked request admitted work")
			}
		})
	}
}
func TestSpritesLibraryRevocationAndQueryBoundaries(t *testing.T) {
	f := artworkFixture(t)
	m := spriteSeed(t, f)
	u, err := f.a.db.CreateUser("sprite-granted", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SetUserLibraries(u.ID, []int64{f.lib}); err != nil {
		t.Fatal(err)
	}
	const token = "sprite-granted"
	if err = f.a.db.SaveAccessTokenForUser(token, u.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	paths := []string{fmt.Sprintf("/api/items/%d/sprites", f.movie), fmt.Sprintf("/api/items/%d/sprites/sheets/0?generation=%s", f.movie, m.Generation), fmt.Sprintf("/Items/%d/ThumbnailSet", f.movie), fmt.Sprintf("/Items/%d/Images/Thumbnail?PositionTicks=0", f.movie), fmt.Sprintf("/Videos/%d/index.bif", f.movie)}
	for _, path := range paths {
		if w := translationCall(f.a, "", "GET", path, ""); w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unauthorized cache boundary %s: %d %v", path, w.Code, w.Header())
		}
		if w := translationCall(f.a, token, "GET", path, ""); w.Code != 200 {
			t.Fatalf("allowed %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if err = f.a.db.SetUserLibraries(u.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if w := translationCall(f.a, token, "GET", path, ""); w.Code != 404 {
			t.Fatalf("revoked %s: %d", path, w.Code)
		}
	}
	for _, query := range []string{"PositionTicks=0&positionticks=0", "positionticks=0&mediasourceid=mediasource_wrong", "PositionTicks=0&Width=320&width=320", "PositionTicks=0&tag=" + m.Generation + "&Tag=" + m.Generation} {
		w := translationCall(f.a, f.token, "GET", fmt.Sprintf("/Items/%d/Images/Thumbnail?%s", f.movie, query), "")
		if w.Code == 200 {
			t.Fatalf("ambiguous/source query accepted %s", query)
		}
	}
	// A virtual multipart ID must never accidentally receive the main STRM's data.
	for _, path := range []string{fmt.Sprintf("/Items/%d:part:1/ThumbnailSet", f.movie), fmt.Sprintf("/Videos/%d:part:1/index.bif", f.movie)} {
		if w := translationCall(f.a, f.token, "GET", path, ""); w.Code != 404 {
			t.Fatalf("multipart %s =>%d", path, w.Code)
		}
	}
}
