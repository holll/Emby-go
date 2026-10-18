package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// 搜索路径的固定基准（DB-04 量化，批次 81）。数据一次播种，benchmark 循环只读。
// 运行：go test ./internal/store -run '^$' -bench 'BenchmarkSearch' -benchmem
var searchBenchOnce sync.Once
var searchBenchStore *Store
var searchBenchErr error

func searchBenchFixture(b *testing.B) *Store {
	b.Helper()
	searchBenchOnce.Do(func() {
		s, err := Open(filepath.Join(b.TempDir(), "search-bench.db"))
		if err != nil {
			searchBenchErr = err
			return
		}
		if _, err := s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(1,'bench','/tmp/bench')"); err != nil {
			searchBenchErr = err
			return
		}
		tx, err := s.db.Begin()
		if err != nil {
			searchBenchErr = err
			return
		}
		const n = 5000
		for i := 0; i < n; i++ {
			title := fmt.Sprintf("测试影片 %d", i)
			if i%3 == 0 {
				title = fmt.Sprintf("Perf Movie %d", i)
			}
			genres := fmt.Sprintf(`["剧情","类型%d"]`, i%5)
			studios := fmt.Sprintf(`["Studio %d"]`, i%3)
			if _, err := tx.Exec(`INSERT INTO movies(library_id,source_path,file_size,file_mtime,source_protocol,source_container,number,status,nfo_path,output_dir,
				title,original_title,plot,year,premiered,rating,director,series,maker,label,collection,official_rating,sortname,taglines,
				provider_id,genres,tags,studios,poster_path,backdrop_path,landscape_path,runtime_seconds,created_at,updated_at)
				VALUES(1,?,0,'2026-01-01T00:00:00Z','','','','success','','',
				?, '', '', 0, '', 0, '', '', '', '', '', '', '', '[]',
				'', ?, '[]', ?, '', '', '', 0, '2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
				fmt.Sprintf("/tmp/bench/f%d.strm", i), title, genres, studios); err != nil {
				searchBenchErr = err
				return
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO actors(name,avatar_url) VALUES(?, '')", fmt.Sprintf("演员%d", i%200)); err != nil {
				searchBenchErr = err
				return
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO movie_actors(movie_id,actor_name) VALUES(?,?)", i+1, fmt.Sprintf("演员%d", i%200)); err != nil {
				searchBenchErr = err
				return
			}
		}
		if err := tx.Commit(); err != nil {
			searchBenchErr = err
			return
		}
		if _, err := s.db.Exec("ANALYZE"); err != nil {
			searchBenchErr = err
			return
		}
		searchBenchStore = s
	})
	if searchBenchErr != nil {
		b.Fatal(searchBenchErr)
	}
	return searchBenchStore
}

func BenchmarkSearchTitleLIKE(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.SearchAdmin(AdminQuery{Term: "测试", Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchGenreJSONLIKE(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.SearchAdmin(AdminQuery{Genre: "剧情", Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchPersonLIKE(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.SearchAdmin(AdminQuery{Person: "演员1", Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchDeepOffset(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.SearchAdmin(AdminQuery{Term: "测试", Limit: 100, Offset: 2000}); err != nil {
			b.Fatal(err)
		}
	}
}
