package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newPlaybackFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(t.TempDir() + "/playback.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	lib, err := s.AddLibrary("playback", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertMovie(Movie{LibraryID: lib.ID, SourcePath: t.TempDir() + "/a.strm", SourceProtocol: "https", Status: "success"}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func record(t *testing.T, s *Store, id int64, key string, kind PlaybackKind, pos int64) PlaybackResult {
	t.Helper()
	res, err := s.RecordPlaybackEvent(PlaybackEvent{SessionKey: key, MovieID: id, Kind: kind, PositionTicks: pos, Now: time.Now()})
	if err != nil {
		t.Fatalf("record %s %s: %v", key, kind, err)
	}
	return res
}

func TestRecordPlaybackStoppedIsIdempotentPerSession(t *testing.T) {
	s, id := newPlaybackFixture(t)
	record(t, s, id, "session-a", PlaybackStart, 0)
	first := record(t, s, id, "session-a", PlaybackStopped, 120)
	if !first.Counted || first.PlayCount != 1 {
		t.Fatalf("first stop: %+v", first)
	}
	second := record(t, s, id, "session-a", PlaybackStopped, 120)
	if second.Counted || second.PlayCount != 1 {
		t.Fatalf("duplicate stop counted: %+v", second)
	}
	// 新会话（新 PlaySessionId）同片同位置再播一次必须计第二次。
	record(t, s, id, "session-b", PlaybackStart, 0)
	third := record(t, s, id, "session-b", PlaybackStopped, 120)
	if !third.Counted || third.PlayCount != 2 {
		t.Fatalf("second session not counted: %+v", third)
	}
	d, err := s.DataUser(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.PlayCount != 2 || d.StoppedTicks != 120 {
		t.Fatalf("userdata=%+v", d)
	}
}

func TestRecordPlaybackStaleProgressDoesNotResurrectSession(t *testing.T) {
	s, id := newPlaybackFixture(t)
	record(t, s, id, "s1", PlaybackStart, 10)
	record(t, s, id, "s1", PlaybackProgress, 20)
	record(t, s, id, "s1", PlaybackStopped, 30)
	// 停止后的迟到 progress：既不复活会话，也不覆盖停止位置。
	rec, err := s.RecordPlaybackEvent(PlaybackEvent{SessionKey: "s1", MovieID: id, Kind: PlaybackProgress, PositionTicks: 5, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Counted {
		t.Fatal("stale progress reported counted")
	}
	record(t, s, id, "s1", PlaybackStopped, 30) // 仍幂等
	d, err := s.DataUser(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.PlayCount != 1 || d.StoppedTicks != 30 || d.PositionTicks != 30 {
		t.Fatalf("stale progress mutated userdata: %+v", d)
	}
	// 新会话开始后同一 play_session 可再次计数（继续观看）。
	record(t, s, id, "s2", PlaybackStart, 0)
	res := record(t, s, id, "s2", PlaybackStopped, 40)
	if !res.Counted || res.PlayCount != 2 {
		t.Fatalf("resumed session: %+v", res)
	}
}

func TestRecordPlaybackFallbackItemKey(t *testing.T) {
	s, id := newPlaybackFixture(t)
	// 客户端未带 PlaySessionId：回退 item:<id>；需要 start 重开才能再次计数。
	record(t, s, id, "", PlaybackStopped, 10)
	res := record(t, s, id, "", PlaybackStopped, 10)
	if res.Counted {
		t.Fatal("duplicate fallback stop counted twice")
	}
	record(t, s, id, "", PlaybackStart, 0)
	again := record(t, s, id, "", PlaybackStopped, 10)
	if !again.Counted || again.PlayCount != 2 {
		t.Fatalf("fallback restart: %+v", again)
	}
}

func TestRecordPlaybackEventsAppendSequence(t *testing.T) {
	s, id := newPlaybackFixture(t)
	record(t, s, id, "seq", PlaybackStart, 0)
	record(t, s, id, "seq", PlaybackProgress, 50)
	record(t, s, id, "seq", PlaybackStopped, 100)
	rows, err := s.db.Query(`SELECT seq,kind FROM playback_events WHERE session_key='seq' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var seq int64
		var kind string
		if err := rows.Scan(&seq, &kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	if len(kinds) != 3 || kinds[0] != "start" || kinds[1] != "progress" || kinds[2] != "stopped" {
		t.Fatalf("events=%v", kinds)
	}
}

func TestRecordPlaybackConcurrentStopsCountOnce(t *testing.T) {
	s, id := newPlaybackFixture(t)
	record(t, s, id, "race", PlaybackStart, 0)
	var wg sync.WaitGroup
	counted := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.RecordPlaybackEvent(PlaybackEvent{SessionKey: "race", MovieID: id, Kind: PlaybackStopped, PositionTicks: 77, Now: time.Now()})
			if err != nil {
				t.Errorf("concurrent stop: %v", err)
				return
			}
			counted <- res.Counted
		}()
	}
	wg.Wait()
	close(counted)
	yes := 0
	for c := range counted {
		if c {
			yes++
		}
	}
	if yes != 1 {
		t.Fatalf("counted %d times, want 1", yes)
	}
	d, err := s.DataUser(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.PlayCount != 1 {
		t.Fatalf("play count=%d", d.PlayCount)
	}
}

func TestRecordPlaybackIdempotentAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	open := func() *Store {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	lib, err := s.AddLibrary("restart", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertMovie(Movie{LibraryID: lib.ID, SourcePath: t.TempDir() + "/a.strm", SourceProtocol: "https", Status: "success"}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.RecordPlaybackEvent(PlaybackEvent{SessionKey: "persisted", MovieID: id, Kind: PlaybackStopped, PositionTicks: 10, Now: time.Now()}); err != nil || res.PlayCount != 1 {
		t.Fatalf("first stop: %+v %v", res, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.Close()
	res, err := s.RecordPlaybackEvent(PlaybackEvent{SessionKey: "persisted", MovieID: id, Kind: PlaybackStopped, PositionTicks: 10, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counted || res.PlayCount != 1 {
		t.Fatalf("replay after restart counted: %+v", res)
	}
}

func recordAs(t *testing.T, s *Store, userID, id int64, key string, kind PlaybackKind, pos int64) PlaybackResult {
	t.Helper()
	res, err := s.RecordPlaybackEvent(PlaybackEvent{UserID: userID, SessionKey: key, MovieID: id, Kind: kind, PositionTicks: pos, Now: time.Now()})
	if err != nil {
		t.Fatalf("record user=%d %s %s: %v", userID, key, kind, err)
	}
	return res
}

// 不同用户使用同一客户端 PlaySessionId 时不得互相停止/覆盖计数（批次 90 P0）。
func TestPlaybackSessionsIsolatedByUser(t *testing.T) {
	s, id := newPlaybackFixture(t)
	if _, err := s.CreateUser("viewer", "password123", false); err != nil {
		t.Fatal(err)
	}
	first := recordAs(t, s, 1, id, "shared-session", PlaybackStopped, 100)
	if !first.Counted || first.PlayCount != 1 {
		t.Fatalf("user1 stop: %+v", first)
	}
	second := recordAs(t, s, 2, id, "shared-session", PlaybackStopped, 100)
	if !second.Counted || second.PlayCount != 1 {
		t.Fatalf("user2 same session key must count independently: %+v", second)
	}
	// 用户 2 重放停止仍是幂等；用户 1 的计数不受影响。
	replay := recordAs(t, s, 2, id, "shared-session", PlaybackStopped, 100)
	if replay.Counted || replay.PlayCount != 1 {
		t.Fatalf("duplicate stop for user2: %+v", replay)
	}
	d1, err := s.DataUser(1, id)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.DataUser(2, id)
	if err != nil {
		t.Fatal(err)
	}
	if d1.PlayCount != 1 || d2.PlayCount != 1 {
		t.Fatalf("userdata isolation: user1=%+v user2=%+v", d1, d2)
	}
	var events int
	var users int
	if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT user_id) FROM playback_events WHERE session_key=?`, "shared-session").Scan(&events, &users); err != nil {
		t.Fatal(err)
	}
	if events != 2 || users != 2 {
		t.Fatalf("events=%d users=%d, want 2/2", events, users)
	}
}
