package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestPlaybackMovieConflictAPI(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			a, id := playbackFixture(t)
			movie, err := a.db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			movie.ID = 0
			movie.SourcePath += ".other"
			other, err := a.db.UpsertMovie(movie, 1, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			reportPlayback(t, a, id, "", "bound", 10)
			if stopped {
				reportPlayback(t, a, id, "/Stopped", "bound", 20)
			}
			beforeA, err := a.db.DataUser(1, id)
			if err != nil {
				t.Fatal(err)
			}
			beforeB, err := a.db.DataUser(1, other)
			if err != nil {
				t.Fatal(err)
			}
			version := a.db.Version("g:version")
			for _, suffix := range []string{"", "/Progress", "/Stopped"} {
				body := fmt.Sprintf(`{"ItemId":%q,"PositionTicks":999,"PlaySessionId":"bound"}`, fmt.Sprint(other))
				w := httptest.NewRecorder()
				c, _ := ginContext(w, httptest.NewRequest(http.MethodPost, "/Sessions/Playing"+suffix, strings.NewReader(body)))
				c.Set(currentUserKey, store.User{ID: 1, IsAdmin: true})
				a.playing(c)
				c.Writer.WriteHeaderNow()
				decodeEnvelope(t, w.Code, w.Body.Bytes(), http.StatusConflict, "PLAYBACK_SESSION_MOVIE_CONFLICT")
			}
			afterA, err := a.db.DataUser(1, id)
			if err != nil {
				t.Fatal(err)
			}
			afterB, err := a.db.DataUser(1, other)
			if err != nil {
				t.Fatal(err)
			}
			if afterA != beforeA || afterB != beforeB || a.db.Version("g:version") != version {
				t.Fatal("conflict changed userdata or cache version")
			}
			reportPlayback(t, a, id, "", "bound", 30)
		})
	}
}
