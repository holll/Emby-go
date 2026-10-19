package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// 迁移 11 的索引与统计信息：删库按 movie_id 级联、登录事件按用户+时间查询。
func TestStatsIndexesAndAnalyze(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, idx := range []string{"idx_userdata_movie", "idx_login_events_user_created"} {
		var name string
		if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&name); err != nil {
			t.Fatalf("索引 %s 不存在: %v", idx, err)
		}
	}

	plan := queryPlan(t, s, "SELECT movie_id FROM userdata WHERE movie_id=1")
	if !strings.Contains(plan, "idx_userdata_movie") {
		t.Errorf("userdata(movie_id) 未走索引:\n%s", plan)
	}
	plan = queryPlan(t, s, "SELECT user_id FROM login_events WHERE user_id=1 ORDER BY created_at DESC")
	if !strings.Contains(plan, "idx_login_events_user_created") {
		t.Errorf("login_events 未走复合索引:\n%s", plan)
	}
	if err := s.Analyze(); err != nil {
		t.Fatalf("analyze: %v", err)
	}
}

func queryPlan(t *testing.T, s *Store, query string) string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return strings.Join(parts, "\n")
}
