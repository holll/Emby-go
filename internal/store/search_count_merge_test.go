package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// searchPage 是下面几个守卫测试共用的调用壳：把 search() 的参数顺序固定下来，
// 让测试里的期望值只表达"分页语义"，不掺参数位置噪声。
func searchPage(t *testing.T, s *Store, libraryID int64, term, genre, sortBy string, limit, offset int) ([]Movie, int) {
	t.Helper()
	out, total, err := s.search(1, libraryID, nil, term, "", "", genre, "", "", "", "", "", false, false, sortBy, false, limit, offset)
	if err != nil {
		t.Fatalf("search(term=%q genre=%q limit=%d offset=%d): %v", term, genre, limit, offset, err)
	}
	return out, total
}

// TestSearchTotalSemanticsAcrossPagination 是 P1-2（把 COUNT(*) 合并进数据查询）的语义守卫。
//
// 期望值全部按**改造前**的行为硬编码（total 由独立的 SELECT COUNT(*) 得到），
// 不由新实现自证：0 行 / 1 行 / 正好一页 / 超一页 / offset 越过末页 / limit=0，
// 每一种都必须同时守住 total、items 长度，以及"0 行时返回 nil 切片"。
func TestSearchTotalSemanticsAcrossPagination(t *testing.T) {
	s, lib := newFeatureStore(t)
	for i := range 5 {
		upsertFixtureMovie(t, s, lib, Movie{
			LibraryID: lib, SourcePath: fmt.Sprintf("/tmp/av/alpha%d.strm", i), Status: "success",
			Title:  fmt.Sprintf("Alpha %d", i),
			Genres: []string{"剧情"},
		})
	}
	upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/av/solo.strm", Status: "success", Title: "Solo"})
	// 非可见状态：不进列表（确保 total 与行集用的是同一套 WHERE）。
	upsertFixtureMovie(t, s, lib, Movie{LibraryID: lib, SourcePath: "/tmp/av/dead.strm", Status: "incompatible", Title: "Alpha dead"})

	cases := []struct {
		name   string
		term   string
		genre  string
		limit  int
		offset int
		want   int // 旧实现在同一查法下返回的 total / 行数
		rows   int
	}{
		// 恰好一页：5 条命中，limit=5
		{"exactly one page", "Alpha", "", 5, 0, 5, 5},
		// limit 大于命中数：一页装下全部
		{"page larger than result", "Alpha", "", 20, 0, 5, 5},
		// 超过一页：第一页 / 最后一页不满 / 正好切在边界
		{"more than one page, first", "Alpha", "", 2, 0, 5, 2},
		{"more than one page, middle", "Alpha", "", 2, 2, 5, 2},
		{"more than one page, last short page", "Alpha", "", 2, 4, 5, 1},
		// 关键：offset 越过末页 —— 行集为空，但 total 仍是真实命中数（旧实现如此）
		{"offset past the end", "Alpha", "", 2, 6, 5, 0},
		{"offset far past the end", "Alpha", "", 10, 100, 5, 0},
		// 关键：limit=0 —— 行集为空，total 仍必须真实
		{"limit zero", "Alpha", "", 0, 0, 5, 0},
		// 1 行命中
		{"single row", "Solo", "", 10, 0, 1, 1},
		{"single row, offset 1", "Solo", "", 10, 1, 1, 0},
		// 0 行命中：total 必须是 0，不能是"上一页的残留"
		{"no match", "Zzzz", "", 10, 0, 0, 0},
		// 带实体过滤（genres）的分页：WHERE 里除了 term 还有 LIKE 子句，合并后 total 仍要正确
		{"with entity filter", "", "剧情", 3, 0, 5, 3},
		{"with entity filter past end", "", "剧情", 3, 9, 5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, total := searchPage(t, s, lib, tc.term, tc.genre, "title", tc.limit, tc.offset)
			if total != tc.want {
				t.Fatalf("total = %d, want %d (old COUNT(*) semantics)", total, tc.want)
			}
			if len(out) != tc.rows {
				t.Fatalf("len(items) = %d, want %d", len(out), tc.rows)
			}
			if tc.rows == 0 && out != nil {
				t.Fatalf("0 rows must return a nil slice (old behaviour), got %#v", out)
			}
			if out != nil && cap(out) > max(min(tc.limit, tc.want), 0) {
				// 预分配必须"一次到位且不过配"：容量不超过 min(limit,total)。
				t.Fatalf("cap = %d, want <= min(limit=%d,total=%d)=%d", cap(out), tc.limit, tc.want, max(min(tc.limit, tc.want), 0))
			}
		})
	}
}

// TestSearchTotalMatchesIndependentCount 用「从播种规则直接算出来的命中集」当 oracle，
// 而不是再用一条 SQL 自证：对每种过滤组合、每个 limit/offset 位置，
// 合并查询返回的 total 必须等于夹具里满足谓词的行数，行数必须等于该页应有的行数。
func TestSearchTotalMatchesIndependentCount(t *testing.T) {
	s, lib := newFeatureStore(t)
	const n = 37
	for i := range n {
		m := Movie{LibraryID: lib, SourcePath: fmt.Sprintf("/tmp/av/x%d.strm", i), Status: "success", Title: fmt.Sprintf("Movie %d", i)}
		if i%2 == 0 {
			m.Genres = []string{"剧情"}
		}
		if i%3 == 0 {
			m.Tags = []string{"热门"}
		}
		upsertFixtureMovie(t, s, lib, m)
	}
	// 播种规则（i%2 决定 genres，i%3 决定 tags，标题固定 "Movie i"）直接算出期望命中数。
	expect := func(term, genre string) int {
		want := 0
		for i := range n {
			title := fmt.Sprintf("Movie %d", i)
			if term != "" && !strings.Contains(title, term) {
				continue
			}
			if genre == "剧情" && i%2 != 0 {
				continue
			}
			want++
		}
		return want
	}
	for _, tc := range []struct{ term, genre string }{
		{"", ""}, {"Movie", ""}, {"", "剧情"}, {"Movie 1", ""}, {"Movie 3", "剧情"},
	} {
		want := expect(tc.term, tc.genre)
		for _, limit := range []int{1, 5, 37, 100, 0} {
			for _, offset := range []int{0, 3, 36, 37, 500} {
				out, total := searchPage(t, s, lib, tc.term, tc.genre, "title", limit, offset)
				if total != want {
					t.Fatalf("term=%q genre=%q limit=%d offset=%d: total = %d, want %d", tc.term, tc.genre, limit, offset, total, want)
				}
				wantRows := max(min(limit, want-offset), 0)
				if len(out) != wantRows {
					t.Fatalf("term=%q genre=%q limit=%d offset=%d: len = %d, want %d", tc.term, tc.genre, limit, offset, len(out), wantRows)
				}
			}
		}
	}
}

// TestSearchEntityFilterTextStable 是 P2-4 的守卫：同一组参数构造 100 次，
// WHERE 文本与参数序列必须逐字节相同（改造前 map range 会随机换序，
// 导致同一查询在 SQLite 里被当成不同语句、无法复用语句缓存与查询计划）。
func TestSearchEntityFilterTextStable(t *testing.T) {
	where, args := searchEntityFilters(nil, nil, "剧情|悬疑", "热门", "Studio 1")
	wantText := strings.Join(where, " AND ")
	wantArgs := append([]any(nil), args...)
	if wantText == "" || len(wantArgs) == 0 {
		t.Fatalf("fixture must produce filters, got where=%q args=%v", wantText, wantArgs)
	}
	for i := range 100 {
		where, args := searchEntityFilters(nil, nil, "剧情|悬疑", "热门", "Studio 1")
		if got := strings.Join(where, " AND "); got != wantText {
			t.Fatalf("iteration %d: WHERE text changed:\n old = %q\n new = %q", i, wantText, got)
		}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("iteration %d: args changed:\n old = %v\n new = %v", i, wantArgs, args)
		}
	}
	// 顺序必须稳定为 genres → tags → studios（不是任意固定顺序，而是这一种）。
	if !strings.HasPrefix(wantText, "(genres LIKE") || !strings.Contains(wantText, " AND (tags LIKE") {
		t.Fatalf("unexpected filter order: %q", wantText)
	}
}

// TestSortColumnMatchesLegacyAllowedMap 固定 SortBy → 排序列的映射，
// 防止把 allowed map 换成 switch 时悄悄改了某个取值。
func TestSortColumnMatchesLegacyAllowedMap(t *testing.T) {
	// 期望值抄自改造前的 allowed map 字面量。
	want := map[string]string{
		"title": "title", "year": "year", "date": "premiered", "sortname": "title",
		"datecreated": "id", "random": "title", "communityrating": "rating",
		// 大小写不敏感 + 未知值回退 title
		"TITLE": "title", "Year": "year", "": "title", "nonsense": "title",
	}
	for in, exp := range want {
		if got := sortColumn(in); got != exp {
			t.Fatalf("sortColumn(%q) = %q, want %q", in, got, exp)
		}
	}
}

// TestMovieScanDecodesMovieColsInOrder 守住 movieScan 与 movieCols 的逐列对应关系。
//
// movieCols 是手写的 37 列清单，movieScan 是手写的 37 个扫描目标；两者顺序一旦
// 错位，扫描本身不会报错（都是可转换的类型），只有值会悄悄串到别的字段上。
// 这里写满一部片的各个区分性字段，扫描后逐字段核对，串列会立刻暴露。
func TestMovieScanDecodesMovieColsInOrder(t *testing.T) {
	s, lib := newFeatureStore(t)
	upsertFixtureMovie(t, s, lib, Movie{
		LibraryID: lib, SourcePath: "/tmp/av/scan.strm", Status: "success", Title: "Scan Me",
		OriginalTitle: "Origin", Plot: "p", Year: 2020, Premiere: "2020-01-01", Rating: 7.5,
		Director: "D", Series: "S", Maker: "M", Label: "L", Collection: "C", OfficialRating: "PG",
		SortName: "scan me", Taglines: []string{"t1"}, ProviderID: "tmdb:1",
		Genres: []string{"剧情"}, Tags: []string{"热门"}, Studios: []string{"Studio 1"},
		PosterPath: "p.jpg", BackdropPath: "b.jpg", LandscapePath: "l.jpg", RuntimeSeconds: 6000,
	})

	rows, err := s.db.Query("SELECT "+movieCols+" FROM movies WHERE id=?", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	// 列数不在这里断言：Scan 的 dest 数量与列数不一致时 SQLite 会直接报错，
	// 真正会"静默错位"的只有顺序问题，由下面逐字段核对覆盖。
	m, err := movieScan(rows)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"ID", m.ID, int64(1)},
		{"LibraryID", m.LibraryID, lib},
		{"SourcePath", m.SourcePath, "/tmp/av/scan.strm"},
		{"Status", m.Status, "success"},
		{"Title", m.Title, "Scan Me"},
		{"OriginalTitle", m.OriginalTitle, "Origin"},
		{"Year", m.Year, 2020},
		{"Premiere", m.Premiere, "2020-01-01"},
		{"Rating", m.Rating, 7.5},
		{"Director", m.Director, "D"},
		{"Series", m.Series, "S"},
		{"Maker", m.Maker, "M"},
		{"Label", m.Label, "L"},
		{"Collection", m.Collection, "C"},
		{"OfficialRating", m.OfficialRating, "PG"},
		{"SortName", m.SortName, "scan me"},
		{"ProviderID", m.ProviderID, "tmdb:1"},
		{"PosterPath", m.PosterPath, "p.jpg"},
		{"BackdropPath", m.BackdropPath, "b.jpg"},
		{"LandscapePath", m.LandscapePath, "l.jpg"},
		{"RuntimeSeconds", m.RuntimeSeconds, int64(6000)},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Fatalf("%s = %#v, want %#v (movieCols/movieScan 列序错位？)", c.name, c.got, c.want)
		}
	}
	if !reflect.DeepEqual(m.Genres, []string{"剧情"}) || !reflect.DeepEqual(m.Tags, []string{"热门"}) ||
		!reflect.DeepEqual(m.Studios, []string{"Studio 1"}) || !reflect.DeepEqual(m.Taglines, []string{"t1"}) {
		t.Fatalf("JSON columns decoded wrong: %+v", m)
	}
}
