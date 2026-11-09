package store

import (
	"path/filepath"
	"testing"
)

func TestVisibleSortIndexMigrationReapplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{
		"idx_movies_visible_year",
		"idx_movies_visible_premiered",
		"idx_movies_visible_created_at",
		"idx_movies_visible_rating",
		"idx_movies_visible_sortname",
	}
	for _, name := range names {
		if _, err := s.db.Exec("DROP INDEX " + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("DELETE FROM schema_migrations WHERE version=21"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range names {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("迁移 21 未补建索引 %s", name)
		}
	}
}

func TestSearchYearFilterUsesIntegerBinding(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "years.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec("INSERT INTO libraries(id,name,path) VALUES(1,'测试库','/tmp')"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		path  string
		year  int
		title string
	}{
		{"/tmp/year-2000.strm", 2000, "Year 2000"},
		{"/tmp/year-2001.strm", 2001, "Year 2001"},
		{"/tmp/year-2001b.strm", 2001, "Year 2001 B"},
	} {
		if _, err := s.db.Exec("INSERT INTO movies(library_id,source_path,status,title,year,created_at,updated_at) VALUES(1,?,'success',?,?, '2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')", row.path, row.title, row.year); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Exec("INSERT INTO movies(library_id,source_path,status,title,year,created_at,updated_at) VALUES(1,'/tmp/year-null.strm','success','No year',NULL,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')")

	movies, total, err := s.search(0, 0, nil, "", "success", "2001", "", "", "", "", "", "", false, false, "year", true, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(movies) != 2 || movies[0].Title != "Year 2001" || movies[1].Title != "Year 2001 B" {
		t.Fatalf("year filter/order=%v total=%d", movies, total)
	}
	movies, total, err = s.search(0, 0, nil, "", "success", "not-a-year", "", "", "", "", "", "", false, false, "year", true, 10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(movies) != 0 {
		t.Fatalf("invalid year filter=%v total=%d", movies, total)
	}
}
