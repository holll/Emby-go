package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"emby-go/internal/scheduler"
	"emby-go/internal/store"
)

type scheduledSpriteParams struct {
	LibraryID int64 `json:"library_id"`
	Limit     int   `json:"limit"`
	Overwrite bool  `json:"overwrite"`
}

// Unlike legacy coercive parsers, explicit null, fractions, strings, unknown
// fields and negative scopes are rejected; zero means all eligible targets.
func parseScheduledSpriteParams(raw map[string]any) (scheduledSpriteParams, error) {
	var p scheduledSpriteParams
	for key, value := range raw {
		if key != "library_id" && key != "limit" && key != "overwrite" {
			return p, fmt.Errorf("unknown sprites parameter %q", key)
		}
		if value == nil {
			return p, fmt.Errorf("sprites %s must not be null", key)
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return p, errors.New("invalid sprites parameters")
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, errors.New("sprites parameters require integer library_id/limit and boolean overwrite")
	}
	if p.LibraryID < 0 || p.Limit < 0 || p.Limit > 1000000 {
		return p, errors.New("sprites library_id must be nonnegative; limit must be 0 to 1000000")
	}
	return p, nil
}

func (a *App) runScheduledSprites(parent context.Context, task scheduler.Task) error {
	p, err := parseScheduledSpriteParams(task.Params)
	if err != nil {
		return err
	}
	if err = parent.Err(); err != nil {
		return err
	}
	if err = a.rootCtx.Err(); err != nil {
		return err
	}
	opts, err := a.spriteOptions()
	if err != nil {
		return errors.New("ffmpeg 或 ffprobe 不可用")
	}
	total, maxID, err := a.db.ScheduledSpriteScope(parent, p.LibraryID)
	if err != nil {
		return errors.New("视频预览媒体库范围不可用")
	}
	if p.Limit > 0 && total > p.Limit {
		total = p.Limit
	}
	a.sprites.mu.Lock()
	if a.rootCtx.Err() != nil || parent.Err() != nil {
		a.sprites.mu.Unlock()
		return context.Canceled
	}
	if a.sprites.status.Running || !a.claimNFO("sprites") {
		a.sprites.mu.Unlock()
		return scheduler.ErrBusy
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	stopParent := context.AfterFunc(parent, cancel)
	defer stopParent()
	id := a.startTask("video_sprites")
	a.sprites.status = spriteStatus{InstanceID: a.instanceID, TaskID: id, MovieIDs: []int64{}, Running: true, Total: total}
	a.sprites.cancel = cancel
	a.sprites.mu.Unlock()
	// A scheduler run is synchronous: cron.Stop waits for it, and manual RunNow
	// is already registered with workerWG by adminRunScheduledTask.
	after := int64(0)
	remaining := total
	var page []store.ScheduledSpriteTarget
	intervals := make(map[int64]int)
	req := spriteRequest{Overwrite: p.Overwrite, next: func(ctx context.Context) (int64, int, bool, error) {
		if remaining == 0 {
			return 0, 0, false, nil
		}
		if len(page) == 0 {
			size := 200
			if remaining < size {
				size = remaining
			}
			var e error
			page, e = a.db.ScheduledSpriteTargets(ctx, p.LibraryID, after, maxID, size)
			if e != nil {
				return 0, 0, false, e
			}
			if len(page) == 0 {
				return 0, 0, false, errors.New("sprite target scope changed during execution")
			}
		}
		target := page[0]
		page = page[1:]
		after = target.ID
		remaining--
		interval, ok := intervals[target.LibraryID]
		if !ok {
			var e error
			interval, e = a.db.LibrarySpriteInterval(ctx, target.LibraryID)
			if e != nil {
				return 0, 0, false, e
			}
			intervals[target.LibraryID] = interval
		}
		return target.ID, interval, true, nil
	}}
	return a.executeSprites(ctx, id, req, opts)
}
