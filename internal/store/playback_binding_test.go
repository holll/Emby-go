package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPlaybackSessionMovieConflictZeroWrites(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		for _, kind := range []PlaybackKind{PlaybackStart, PlaybackProgress, PlaybackStopped} {
			t.Run(fmt.Sprintf("stopped=%t/%s", stopped, kind), func(t *testing.T) {
				s, a := newPlaybackFixture(t)
				movie, err := s.Movie(a)
				if err != nil {
					t.Fatal(err)
				}
				movie.ID = 0
				movie.SourcePath += ".other"
				b, err := s.IndexMovie(movie, 1, time.Now(), nil)
				if err != nil {
					t.Fatal(err)
				}
				record(t, s, a, "bound", PlaybackStart, 10)
				if stopped {
					record(t, s, a, "bound", PlaybackStopped, 20)
				}
				// Snapshot every column in all three affected tables, not only the count.
				snapshot := func() string {
					var out string
					for _, table := range []string{"playback_sessions", "playback_events", "userdata"} {
						rows, err := s.db.Query("SELECT * FROM " + table + " ORDER BY rowid")
						if err != nil {
							t.Fatal(err)
						}
						cols, err := rows.Columns()
						if err != nil {
							t.Fatal(err)
						}
						for rows.Next() {
							vals := make([]any, len(cols))
							dst := make([]any, len(cols))
							for i := range vals {
								dst[i] = &vals[i]
							}
							if err := rows.Scan(dst...); err != nil {
								t.Fatal(err)
							}
							out += fmt.Sprintf("%s:%#v\n", table, vals)
						}
						if err := rows.Err(); err != nil {
							t.Fatal(err)
						}
						rows.Close()
					}
					return out
				}
				before := snapshot()
				got, err := s.RecordPlaybackEvent(PlaybackEvent{UserID: 1, MovieID: b, SessionKey: "bound", Kind: kind, PositionTicks: 999})
				if !errors.Is(err, ErrPlaybackSessionMovieConflict) || got != (PlaybackResult{}) {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				if after := snapshot(); after != before {
					t.Fatalf("conflict mutated tables\nbefore=%s\nafter=%s", before, after)
				}
				// Same movie remains usable; a different user may reuse the same key.
				record(t, s, a, "bound", PlaybackStart, 30)
				seedFixtureUser(t, s, "owner", "hash", true)
				u := seedFixtureUser(t, s, "other", "hash", false)
				if _, err := s.RecordPlaybackEvent(PlaybackEvent{UserID: u.ID, MovieID: b, SessionKey: "bound", Kind: PlaybackStart}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestPlaybackConcurrentFirstEventsBindOneMovie(t *testing.T) {
	s, a := newPlaybackFixture(t)
	movie, err := s.Movie(a)
	if err != nil {
		t.Fatal(err)
	}
	movie.ID = 0
	movie.SourcePath += ".other"
	b, err := s.IndexMovie(movie, 1, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []int64{a, b} {
		wg.Go(func() {
			<-start
			_, err := s.RecordPlaybackEvent(PlaybackEvent{UserID: 1, MovieID: id, SessionKey: "first", Kind: PlaybackStopped})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, conflicts := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrPlaybackSessionMovieConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("accepted=%d conflicts=%d", accepted, conflicts)
	}
	var sessions, events, counts int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM playback_sessions),(SELECT count(*) FROM playback_events),(SELECT sum(play_count) FROM userdata)`).Scan(&sessions, &events, &counts); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || events != 1 || counts != 1 {
		t.Fatalf("sessions=%d events=%d counts=%d", sessions, events, counts)
	}
}
