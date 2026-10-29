package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PlaybackKind 是播放事件类型；对应 Emby 的 Playing / Progress / Stopped。
type PlaybackKind string

const (
	PlaybackStart    PlaybackKind = "start"
	PlaybackProgress PlaybackKind = "progress"
	PlaybackStopped  PlaybackKind = "stopped"
)

const (
	// playbackEventsRetain 是 playback_events 的行数上界：事件表只用于审计/
	// 排障（序列号复检），保留最近 2 万条即可，避免长跑服务被心跳无限撑大。
	playbackEventsRetain = 20000
	// playbackPruneEvery 是两次 prune 之间的事件数窗口（P2-12）。原来每条事件都
	// 在写事务里跑一次 `NOT IN (…2 万行…)`，等于每条事件多一次 2 万行子查询，还
	// 和扫描提交争同一把写锁；按窗口触发把这份代价摊到 N 条事件上。
	playbackPruneEvery = 64
)

// playbackPruneHook 仅测试使用（生产恒为 nil）：每真正执行一次 prune 前调用，
// 用来断言「连续 N 条记录只 prune 一次」。
var playbackPruneHook func()

// maybePrunePlaybackEvents 在事件 id 命中 prune 窗口时把事件表裁回 retain 行。
//
// 为什么用事件 id 取模、而不是内存计数器：writeTx 在 SQLITE_BUSY 时会重跑整个
// 回调，内存计数器会被重跑重复推进（窗口错位、可能根本不 prune）；而 id 由
// SQLite 分配、每次提交后单调增大，同一条事件无论重跑多少次都落在同一个窗口里，
// 触发是幂等的，因此并发与重试下不会多 prune，也不会漏掉窗口。
//
// 为什么先删改成 id 范围删：`DELETE … WHERE id <= (… ORDER BY id DESC LIMIT 1
// OFFSET retain)` 只需要沿主键索引倒序走 retain 步，不像 NOT IN 子查询那样把
// retain 个 id 全部物化再逐行比对。行数上界语义不变：只有当表里已有 retain+1
// 行时才会删掉最老的那 1 行，稳态下 ≤ retain 行；两次 prune 之间最多再多积
// playbackPruneEvery-1 行，上界是 retain+playbackPruneEvery-1，不会被无限撑大。
func maybePrunePlaybackEvents(tx *sql.Tx, eventID int64) error {
	if eventID <= 0 || eventID%playbackPruneEvery != 0 {
		return nil
	}
	if playbackPruneHook != nil {
		playbackPruneHook()
	}
	// OFFSET 之外没有行时子查询为 NULL，`id <= NULL` 匹配 0 行：表未超过上界时
	// 这条 DELETE 是空操作，不需要先 COUNT。
	if _, err := tx.Exec(`DELETE FROM playback_events WHERE id <= (SELECT id FROM playback_events ORDER BY id DESC LIMIT 1 OFFSET ?)`, playbackEventsRetain); err != nil {
		return fmt.Errorf("prune playback events: %w", err)
	}
	return nil
}

// PlaybackEvent 是一次播放上报。SessionKey 为空时回退为 item:<movie_id>，
// 此时同片多次播放依赖客户端先发 start 重开会话；有 PlaySessionId 时按会话隔离。
type PlaybackEvent struct {
	SessionKey    string
	UserID        int64
	MovieID       int64
	DeviceID      string
	Kind          PlaybackKind
	PositionTicks int64
	Now           time.Time
}

// PlaybackResult 返回本次上报后的播放次数，以及是否发生了计数（仅 stopped）。
type PlaybackResult struct {
	PlayCount int64
	Counted   bool
	// Accepted distinguishes committed events from ignored duplicate stops/late progress.
	Accepted bool
}

// RecordPlaybackEvent 在单事务内应用一次播放上报：
//   - start/progress 只在会话未停止时推进位置；停止后的迟到心跳直接忽略，
//     不会把 stopped 标记或 last_stopped_ticks 覆盖回未停止状态。
//   - stopped 只在会话从 stopped=0 迁移到 1 时使 play_count 原子 +1；
//     同一会话重复 stopped / 重放同一事件不会重复计数。
//   - 每个被接受的事件写入 playback_events，seq 为会话内单调序号。
func (s *Store) RecordPlaybackEvent(ev PlaybackEvent) (PlaybackResult, error) {
	key := ev.SessionKey
	if key == "" {
		key = fmt.Sprintf("item:%d", ev.MovieID)
	}
	now := ev.Now
	if now.IsZero() {
		now = time.Now()
	}
	nowText := now.UTC().Format(time.RFC3339)
	userID := ev.UserID
	if userID <= 0 {
		userID = 1
	}
	var out PlaybackResult
	err := s.writeTx(func(tx *sql.Tx) error {
		switch ev.Kind {
		case PlaybackStopped:
			var seq int64
			err := tx.QueryRow(`INSERT INTO playback_sessions(session_key,user_id,movie_id,device_id,started_at,last_event_seq,position_ticks,stopped,stopped_at)
				VALUES(?,?,?,?,?,1,?,1,?)
				ON CONFLICT(user_id,session_key) DO UPDATE SET stopped=1,
					last_event_seq=playback_sessions.last_event_seq+1,
					position_ticks=excluded.position_ticks, device_id=excluded.device_id, stopped_at=excluded.stopped_at
				WHERE playback_sessions.stopped=0
				RETURNING last_event_seq`,
				key, userID, ev.MovieID, ev.DeviceID, nowText, ev.PositionTicks, nowText).Scan(&seq)
			if errors.Is(err, sql.ErrNoRows) {
				// 同一会话重复停止（含客户端重试）：幂等，不计数、不写事件。
				return tx.QueryRow(`SELECT COALESCE(play_count,0) FROM userdata WHERE user_id=? AND movie_id=?`, userID, ev.MovieID).Scan(&out.PlayCount)
			}
			if err != nil {
				return err
			}
			var eventID int64
			if err := tx.QueryRow(`INSERT INTO playback_events(user_id,session_key,seq,kind,position_ticks,created_at) VALUES(?,?,?,?,?,?) RETURNING id`,
				userID, key, seq, string(ev.Kind), ev.PositionTicks, nowText).Scan(&eventID); err != nil {
				return err
			}
			// P2-12: prune 与事件写入同一事务（失败一起回滚），但只在窗口命中时执行。
			if err := maybePrunePlaybackEvents(tx, eventID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO userdata(user_id,movie_id,position_ticks,play_count,last_played_at,last_stopped_ticks)
				VALUES(?,?,?,1,?,?)
				ON CONFLICT(user_id,movie_id) DO UPDATE SET position_ticks=excluded.position_ticks,
					play_count=userdata.play_count+1, last_played_at=excluded.last_played_at, last_stopped_ticks=excluded.last_stopped_ticks`,
				userID, ev.MovieID, ev.PositionTicks, nowText, ev.PositionTicks); err != nil {
				return err
			}
			out.Accepted = true
			out.Counted = true
			return tx.QueryRow(`SELECT play_count FROM userdata WHERE user_id=? AND movie_id=?`, userID, ev.MovieID).Scan(&out.PlayCount)
		case PlaybackStart, PlaybackProgress:
			var seq int64
			err := tx.QueryRow(`INSERT INTO playback_sessions(session_key,user_id,movie_id,device_id,started_at,last_event_seq,position_ticks,stopped)
				VALUES(?,?,?,?,?,1,?,0)
				ON CONFLICT(user_id,session_key) DO UPDATE SET last_event_seq=playback_sessions.last_event_seq+1,
					position_ticks=excluded.position_ticks, device_id=excluded.device_id,
					started_at=CASE WHEN ?='start' THEN excluded.started_at ELSE playback_sessions.started_at END,
					stopped=CASE WHEN ?='start' THEN 0 ELSE playback_sessions.stopped END
				WHERE ?='start' OR playback_sessions.stopped=0
				RETURNING last_event_seq`,
				key, userID, ev.MovieID, ev.DeviceID, nowText, ev.PositionTicks, string(ev.Kind), string(ev.Kind), string(ev.Kind)).Scan(&seq)
			if errors.Is(err, sql.ErrNoRows) {
				// 停止后的迟到 Progress：忽略，不复活会话、不覆盖停止标记。
				return nil
			}
			if err != nil {
				return err
			}
			var eventID int64
			if err := tx.QueryRow(`INSERT INTO playback_events(user_id,session_key,seq,kind,position_ticks,created_at) VALUES(?,?,?,?,?,?) RETURNING id`,
				userID, key, seq, string(ev.Kind), ev.PositionTicks, nowText).Scan(&eventID); err != nil {
				return err
			}
			// 心跳事件同样推进 prune 窗口：只靠 stop 触发的话，一直不发 stop 的
			// 客户端会让事件表绕过上界（P2-12）。
			if err := maybePrunePlaybackEvents(tx, eventID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO userdata(user_id,movie_id,position_ticks,last_played_at) VALUES(?,?,?,?)
				ON CONFLICT(user_id,movie_id) DO UPDATE SET position_ticks=excluded.position_ticks, last_played_at=excluded.last_played_at`,
				userID, ev.MovieID, ev.PositionTicks, nowText); err != nil {
				return err
			}
			out.Accepted = true
			return nil
		default:
			return fmt.Errorf("unknown playback event kind %q", ev.Kind)
		}
	})
	if err != nil {
		return PlaybackResult{}, err
	}
	return out, nil
}
