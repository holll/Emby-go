package store

import "time"

// EditFailure 是「NFO 已保存、索引未完成」的待恢复条目。
type EditFailure struct {
	MovieID       int64  `json:"movie_id"`
	Title         string `json:"title"`
	Code          string `json:"code"`
	Attempts      int64  `json:"attempts"`
	FirstFailedAt string `json:"first_failed_at"`
	LastFailedAt  string `json:"last_failed_at"`
	AgeDays       int    `json:"age_days"`
}

// MarkEditFailure 幂等记录一次索引失败：首次失败时间保留，次数与最近时间推进。
func (s *Store) MarkEditFailure(id int64, code string, now time.Time) error {
	at := now.UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO edit_failures(movie_id,code,attempts,first_failed_at,last_failed_at)
		VALUES(?,?,1,?,?)
		ON CONFLICT(movie_id) DO UPDATE SET code=excluded.code,attempts=edit_failures.attempts+1,last_failed_at=excluded.last_failed_at`,
		id, code, at, at)
	return err
}

// ClearEditFailure 在索引恢复成功后删除条目；不存在时不算错误。
func (s *Store) ClearEditFailure(id int64) error {
	_, err := s.db.Exec("DELETE FROM edit_failures WHERE movie_id=?", id)
	return err
}

// EditFailures 返回仍待恢复的条目，按最近失败时间升序（最旧在前）。
func (s *Store) EditFailures(now time.Time) ([]EditFailure, error) {
	rows, err := s.db.Query(`SELECT f.movie_id,COALESCE(m.title,''),f.code,f.attempts,f.first_failed_at,f.last_failed_at
		FROM edit_failures f LEFT JOIN movies m ON m.id=f.movie_id ORDER BY f.last_failed_at ASC, f.movie_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EditFailure
	for rows.Next() {
		var f EditFailure
		if err := rows.Scan(&f.MovieID, &f.Title, &f.Code, &f.Attempts, &f.FirstFailedAt, &f.LastFailedAt); err != nil {
			return nil, err
		}
		if at, err := time.Parse(time.RFC3339, f.FirstFailedAt); err == nil {
			f.AgeDays = int(now.Sub(at).Hours() / 24)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// EditFailureCounts 返回待恢复总数与超过 7 天的数量，供提示使用。
func (s *Store) EditFailureCounts(now time.Time, staleAfter time.Duration) (total, stale int, err error) {
	failures, err := s.EditFailures(now)
	if err != nil {
		return 0, 0, err
	}
	cutoff := now.Add(-staleAfter)
	for _, f := range failures {
		if at, err := time.Parse(time.RFC3339, f.FirstFailedAt); err == nil && at.Before(cutoff) {
			stale++
		}
	}
	return len(failures), stale, nil
}
