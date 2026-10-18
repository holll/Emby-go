package store

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSecondaryIndexesPresent 锁定批次 80 的三个索引，避免迁移回退时静默丢失。
func TestSecondaryIndexesPresent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "indexes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{
		"idx_movie_actors_actor_movie",
		"idx_movies_visible_title",
		"idx_movies_visible_library_title",
	} {
		var sql string
		if err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&sql); err != nil {
			t.Fatalf("索引 %s 缺失: %v", name, err)
		}
	}
}

// TestSecondaryIndexQueryPlans 用 EXPLAIN QUERY PLAN 验证列表与演员查询确实吃到索引。
func TestSecondaryIndexQueryPlans(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 统计信息影响计划选择：写入足够行并 ANALYZE，模拟真实库而不是空表默认计划。
	for i := 1; i <= 2; i++ {
		if _, err := s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(?,?,?)", i, "测试"+strconv.Itoa(i), "/tmp/lib"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		if _, err := tx.Exec("INSERT INTO movies(library_id,source_path,status,title,created_at,updated_at) VALUES(?,?, 'success',?, '2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')",
			1+i%2, "/tmp/lib/f"+strconv.Itoa(i)+".strm", "标题 "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT INTO actors(name,avatar_url) VALUES(?, '') ON CONFLICT(name) DO NOTHING", "演员"+strconv.Itoa(i%40)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO movie_actors(movie_id,actor_name) VALUES(?,?)", i+1, "演员"+strconv.Itoa(i%40)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ANALYZE"); err != nil {
		t.Fatal(err)
	}
	plans := []struct {
		name      string
		index     string
		statement string
	}{
		{
			name:      "全局默认列表去临时排序",
			index:     "idx_movies_visible_title",
			statement: "EXPLAIN QUERY PLAN SELECT id FROM movies WHERE status IN ('success','manual') ORDER BY title ASC, id LIMIT 100 OFFSET 2000",
		},
		{
			name:      "单库列表按库+标题",
			index:     "idx_movies_visible_library_title",
			statement: "EXPLAIN QUERY PLAN SELECT id FROM movies WHERE status IN ('success','manual') AND library_id=1 ORDER BY title ASC, id LIMIT 100",
		},
		{
			name:      "演员起步的头像候选连接",
			index:     "idx_movie_actors_actor_movie",
			statement: "EXPLAIN QUERY PLAN SELECT DISTINCT a.name FROM actors a JOIN movie_actors ma ON ma.actor_name=a.name JOIN movies m ON m.id=ma.movie_id WHERE COALESCE(a.avatar_url,'')='' AND m.status IN ('success','manual') ORDER BY a.name",
		},
	}
	for _, tc := range plans {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.db.Query(tc.statement)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var all []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatal(err)
				}
				all = append(all, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(all, " | "), tc.index) {
				t.Fatalf("查询计划未使用 %s: %v", tc.index, all)
			}
		})
	}
}

// TestSecondaryIndexMigrationReapplies 模拟旧库（缺迁移 6 的 ledger/索引）重新打开后补跑。
func TestSecondaryIndexMigrationReapplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_movie_actors_actor_movie", "idx_movies_visible_title", "idx_movies_visible_library_title"} {
		if _, err := s.db.Exec("DROP INDEX " + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("DELETE FROM schema_migrations WHERE version=6"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"idx_movie_actors_actor_movie", "idx_movies_visible_title", "idx_movies_visible_library_title"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("迁移 6 未补建索引 %s", name)
		}
	}
}
