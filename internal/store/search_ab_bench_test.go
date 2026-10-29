package store

import (
	"database/sql"
	"testing"
)

// 本文件是 P1-1（append 预分配）与 P1-2（COUNT 合并进数据查询）的对照基准。
//
// 为什么要单独写 legacy 版本：改造前的绝对数字是在另一天、另一台负载下采的
// （load 0.22–0.28 vs 现在的 1–2），跨窗口比 ns/op 没有意义。这里把旧路径
// 逐字复制成基准专用实现，让"旧 vs 新"在同一个进程、相邻的时间窗里跑，
// 并且拆成三个变体，把收益按来源归因：
//
//	legacyCountLegacyScan —— 改造前：COUNT(*) → 数据查询 → movieScan → append 无预分配
//	legacyCountPrealloc  —— 只做 P1-1：COUNT(*) → 数据查询 → movieScan → min(limit,total) 预分配
//	windowPrealloc       —— 被否决的方案：单条 COUNT(*) OVER () 查询 → 预分配
//
// 三个变体用的是同一份 SQL 文本与同一组参数（与 search() 实际拼出来的一致），
// 因此 B/op 的差值可以直接归因：预分配省掉 append 扩容，合并省掉一次全表扫描。

const benchSearchWhere = ` WHERE status IN ('success','manual') AND (title LIKE ? ESCAPE '\' OR original_title LIKE ? ESCAPE '\' OR number LIKE ? ESCAPE '\')`

// benchMovieScanTotal 是"被否决方案"专用的扫描：多读一列 COUNT(*) OVER ()。
// 生产代码里没有它（见 search() 的注释：合并被实测否决），基准里保留一份，
// 是为了让"为什么否决"这件事可以随时复现。
func benchMovieScanTotal(row *sql.Rows, total *int) (Movie, error) {
	var m Movie
	var genres, tags, studios, taglines, parts, mt string
	err := row.Scan(&m.ID, &m.LibraryID, &m.SourcePath, &mt, &m.SourceProtocol, &m.SourceContainer, &m.Number, &m.Status, &m.NFOPath, &m.OutputDir, &m.Title, &m.OriginalTitle, &m.Plot, &m.Year, &m.Premiere, &m.Rating, &m.Director, &m.Series, &m.Maker, &m.Label, &m.Collection, &m.OfficialRating, &m.SortName, &taglines, &m.ProviderID, &genres, &tags, &studios, &m.PosterPath, &m.BackdropPath, &m.LandscapePath, &m.RuntimeSeconds, &parts, &m.CreatedAt, &m.UpdatedAt, &m.LastScrapeAt, &m.LastScrapeError, total)
	m.Genres = parseStrings(genres)
	m.Tags = parseStrings(tags)
	m.Studios = parseStrings(studios)
	m.Taglines = parseStrings(taglines)
	m.AdditionalParts = parseStrings(parts)
	return m, err
}

func benchSearchArgs(limit, offset int) []any {
	return []any{"%测试%", "%测试%", "%测试%", limit, offset}
}

func benchCountArgs() []any {
	return []any{"%测试%", "%测试%", "%测试%"}
}

// legacyCountLegacyScan 是改造前 search() 尾部的逐字副本。
func legacyCountLegacyScan(b *testing.B, s *Store, limit, offset int) {
	b.Helper()
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movies"+benchSearchWhere, benchCountArgs()...).Scan(&total); err != nil {
		b.Fatal(err)
	}
	rows, err := s.db.Query("SELECT "+movieCols+" FROM movies"+benchSearchWhere+" ORDER BY title ASC,id LIMIT ? OFFSET ?", benchSearchArgs(limit, offset)...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, e := movieScan(rows)
		if e != nil {
			b.Fatal(e)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	want := 0
	if limit > 0 {
		want = min(limit, max(total-offset, 0))
	}
	if len(out) != want {
		b.Fatalf("unexpected row count %d (total=%d want=%d)", len(out), total, want)
	}
}

// legacyCountPrealloc 只把 append 换成预分配（P1-1 单独收益）。
func legacyCountPrealloc(b *testing.B, s *Store, limit, offset int) {
	b.Helper()
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movies"+benchSearchWhere, benchCountArgs()...).Scan(&total); err != nil {
		b.Fatal(err)
	}
	rows, err := s.db.Query("SELECT "+movieCols+" FROM movies"+benchSearchWhere+" ORDER BY title ASC,id LIMIT ? OFFSET ?", benchSearchArgs(limit, offset)...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	out := make([]Movie, 0, max(min(limit, total), 0))
	for rows.Next() {
		m, e := movieScan(rows)
		if e != nil {
			b.Fatal(e)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
}

// windowPrealloc 是被否决的"合并 COUNT"方案（P1-2）。
// 保留它是为了让否决理由可复现：它比两条语句的版本慢 6~7 倍，见 search() 里的注释。
func windowPrealloc(b *testing.B, s *Store, limit, offset int) {
	b.Helper()
	rows, err := s.db.Query("SELECT "+movieCols+",COUNT(*) OVER () FROM movies"+benchSearchWhere+" ORDER BY title ASC,id LIMIT ? OFFSET ?", benchSearchArgs(limit, offset)...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		var total int
		m, e := benchMovieScanTotal(rows, &total)
		if e != nil {
			b.Fatal(e)
		}
		if out == nil {
			out = make([]Movie, 0, max(min(limit, total), 0))
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkSearchPage100LegacyCountLegacyScan(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		legacyCountLegacyScan(b, s, 100, 0)
	}
}

func BenchmarkSearchPage100LegacyCountPrealloc(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		legacyCountPrealloc(b, s, 100, 0)
	}
}

func BenchmarkSearchPage100WindowPrealloc(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		windowPrealloc(b, s, 100, 0)
	}
}

// 下面两个基准把旧路径拆成两段，用来解释"为什么不能用 COUNT(*) OVER () 合并"：
// COUNT 与数据查询各自的成本，以及窗口函数版单条查询的成本。
func BenchmarkSearchPage100CountOnly(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var total int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM movies"+benchSearchWhere, benchCountArgs()...).Scan(&total); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchPage100DataOnlyLegacyScan(b *testing.B) {
	s := searchBenchFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := s.db.Query("SELECT "+movieCols+" FROM movies"+benchSearchWhere+" ORDER BY title ASC,id LIMIT ? OFFSET ?", benchSearchArgs(100, 0)...)
		if err != nil {
			b.Fatal(err)
		}
		var out []Movie
		for rows.Next() {
			m, e := movieScan(rows)
			if e != nil {
				b.Fatal(e)
			}
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		rows.Close()
	}
}
