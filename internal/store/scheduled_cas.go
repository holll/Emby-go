package store

import (
	"database/sql"
	"errors"
	"time"
)

var ErrScheduledConfigChanged = errors.New("scheduled configuration changed")

// CompareAndSwapScheduledTask compares editable configuration, not timestamps or
// last-run results. RETURNING captures exactly this write, not a subsequent edit.
// Equal-content ABA is intentionally accepted; this is not a revision counter.
func (s *Store) CompareAndSwapScheduledTask(expected, replacement ScheduledTask) (ScheduledTask, error) {
	if replacement.Params == "" {
		replacement.Params = "{}"
	}
	row := s.db.QueryRow(`UPDATE scheduled_tasks SET name=?,type=?,cron=?,params=?,enabled=?,updated_at=?
 WHERE id=? AND name=? AND type=? AND cron=? AND params=? AND enabled=? AND created_at=? RETURNING `+scheduledColumns,
		replacement.Name, replacement.Type, replacement.Cron, replacement.Params, replacement.Enabled, time.Now().UTC().Format(time.RFC3339),
		expected.ID, expected.Name, expected.Type, expected.Cron, expected.Params, expected.Enabled, expected.CreatedAt)
	updated, err := scanScheduled(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduledTask{}, ErrScheduledConfigChanged
	}
	return updated, err
}
