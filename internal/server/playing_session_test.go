package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/store"
)

func playbackFixture(t *testing.T) (*App, int64) {
	t.Helper()
	root := t.TempDir()
	a, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	lib, err := a.db.AddLibrary("playback", root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.db.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "a.strm"), SourceProtocol: "https", Status: "success"}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return a, id
}

func reportPlayback(t *testing.T, a *App, id int64, event, sessionID string, position int64) {
	t.Helper()
	body := fmt.Sprintf(`{"ItemId":%q,"PositionTicks":%d,"PlaySessionId":%q}`, fmt.Sprint(id), position, sessionID)
	w := httptest.NewRecorder()
	c, _ := ginContext(w, httptest.NewRequest(http.MethodPost, "/Sessions/Playing"+event, strings.NewReader(body)))
	a.playing(c)
	c.Writer.WriteHeaderNow()
	if w.Code != http.StatusNoContent {
		t.Fatalf("%s/%s status=%d body=%s", event, sessionID, w.Code, w.Body.String())
	}
}

// D07：同一影片不同 PlaySessionId 各计一次；旧实现靠「停止位置是否变化」判重，
// 同位置重播会被漏计。
func TestPlaybackSessionsCountIndependently(t *testing.T) {
	a, id := playbackFixture(t)
	reportPlayback(t, a, id, "", "session-1", 0)
	reportPlayback(t, a, id, "/Stopped", "session-1", 120)
	reportPlayback(t, a, id, "", "session-2", 0)
	reportPlayback(t, a, id, "/Stopped", "session-2", 120)
	d, err := a.db.Data(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.PlayCount != 2 {
		t.Fatalf("play count=%d, want 2 (distinct sessions)", d.PlayCount)
	}
	// 重放同一 stopped 事件仍幂等。
	reportPlayback(t, a, id, "/Stopped", "session-2", 120)
	if d, _ = a.db.Data(id); d.PlayCount != 2 {
		t.Fatalf("replayed stop changed count=%d", d.PlayCount)
	}
}

// D07：停止后的迟到心跳不能把停止标记覆盖成未停止，否则下一次 Stopped
// 会再计一次（旧实现正是这样把 play_count 抬高的）。
func TestPlaybackStaleProgressCannotUnstop(t *testing.T) {
	a, id := playbackFixture(t)
	reportPlayback(t, a, id, "", "stale", 0)
	reportPlayback(t, a, id, "/Progress", "stale", 60)
	reportPlayback(t, a, id, "/Stopped", "stale", 100)
	reportPlayback(t, a, id, "/Progress", "stale", 10) // 迟到
	reportPlayback(t, a, id, "/Stopped", "stale", 100)
	d, err := a.db.Data(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.PlayCount != 1 || d.StoppedTicks != 100 || d.PositionTicks != 100 {
		t.Fatalf("stale progress corrupted userdata: %+v", d)
	}
}
